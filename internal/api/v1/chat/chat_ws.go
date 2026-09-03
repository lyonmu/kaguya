package chat

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// ChatWS
// @Tags      Chat
// @Summary   ChatWS
// @Description WebSocket 流式对话：长连接多轮对话。上行帧 ChatWSReq（flag=chat/cancel），下行帧 dtocode.Response 包 ChatWSResp（flag=start/delta/done/error）
// @Produce   json
// @Success   200  {object}  dtocode.Response{code=number,data=dtochat.ChatResp,message=string}  "WS 帧，每帧为一个 dtocode.Response"
// @Router    /v1/chat/ws [GET]
func (b *ChatApiV1Group) ChatWS(c *gin.Context) {
	conn, err := pkg.Upgrade(c, pkg.WithReadLimit(1<<20))
	if err != nil {
		global.Logger.Sugar().Errorf("websocket upgrade failed, err is %+v", err)
		return
	}
	defer conn.Close()

	connCtx := c.Request.Context()

	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		busy       bool
		turnCancel context.CancelFunc
	)

	send := make(chan *dtocode.Response)

	// 唯一写 goroutine：串行写出所有下行帧
	go func() {
		for resp := range send {
			if err := conn.WriteJSON(resp); err != nil {
				global.Logger.Sugar().Errorf("websocket write failed, err is %+v", err)
				mu.Lock()
				if turnCancel != nil {
					turnCancel()
				}
				mu.Unlock()
				_ = conn.Close()
				return
			}
		}
	}()

	// 投递错误帧（读循环使用连接级 ctx）
	sendErr := func(resp dtocode.Response) {
		resp.Data = dtochat.ChatResp{Chat: dtochat.Chat{Flag: dtochat.WSFlagError}}
		select {
		case <-connCtx.Done():
		case send <- &resp:
		}
	}

	for {
		var req dtochat.ChatReq
		if err := conn.ReadJSON(&req); err != nil {
			// 连接关闭 / 读错误：取消当前轮并终结写 goroutine
			mu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			mu.Unlock()
			wg.Wait() // 等所有转发 goroutine 退出后再 close，避免 send-on-closed-channel
			close(send)
			return
		}

		switch req.Flag {
		case dtochat.WSFlagChat:
			mu.Lock()
			if busy {
				mu.Unlock()
				sendErr(dtocode.ChatWSBusy)
				continue
			}
			mu.Unlock()

			if req.Messages == "" {
				sendErr(dtocode.RequestParameterError)
				continue
			}

			turnCtx, cancel := context.WithCancel(connCtx)
			mu.Lock()
			busy = true
			turnCancel = cancel
			mu.Unlock()

			dataChan := make(chan *dtochat.ChatResp)
			go agentvc.Chat(turnCtx, dataChan, &dtochat.ChatReq{
				ID:       req.ID,
				Messages: req.Messages,
			})

			// 每轮转发 goroutine：dataChan → send，映射 flag
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					mu.Lock()
					busy = false
					turnCancel = nil
					mu.Unlock()
				}()

				first := true
				for v := range dataChan {
					resp, _ := mapFrame(v, first)
					first = false

					select {
					case <-turnCtx.Done():
						return
					case send <- &resp:
					}
				}
			}()

		case dtochat.WSFlagCancel:
			mu.Lock()
			canceled := busy && turnCancel != nil
			if canceled {
				turnCancel()
			}
			mu.Unlock()
			if canceled {
				// 读循环直接投递取消确认帧：Service 在 ctx 取消后不再投递任何帧
				sendErr(dtocode.ChatWSCanceled)
			}

		default:
			sendErr(dtocode.RequestParameterError)
		}
	}
}

// mapFrame 将一条 ChatResp 映射为下行 dtocode.Response（SSE 与 WS 共用），
// 并按帧类型填充 Chat.Flag（start/delta/done/error）。
// 返回的 bool 表示是否为错误帧（error 分支仅带 flag，不带 ChatResp）。
func mapFrame(v *dtochat.ChatResp, first bool) (dtocode.Response, bool) {
	resp := dtocode.SystemSuccess
	if v.Err != nil {
		resp = dtocode.ChatSSEFailure
		resp.Data = dtochat.ChatResp{Chat: dtochat.Chat{Flag: dtochat.WSFlagError}}
		return resp, true
	}
	flag := dtochat.WSFlagDelta
	if first {
		flag = dtochat.WSFlagStart
	} else if v.Usage.TotalTokens > 0 {
		flag = dtochat.WSFlagDone
	}
	v.Chat.Flag = flag
	resp.Data = v
	return resp, false
}
