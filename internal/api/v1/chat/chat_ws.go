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
// @Success   200  {object}  dtocode.Response{code=number,data=dtochat.ChatWSResp,message=string}  "WS 帧，每帧为一个 dtocode.Response"
// @Router    /v1/chat/ws [GET]
func (b *ChatApiV1Group) ChatWS(c *gin.Context) {
	conn, err := pkg.Upgrade(c)
	if err != nil {
		global.Logger.Sugar().Errorf("websocket upgrade failed, err is %+v", err)
		return
	}
	defer conn.Close()

	connCtx := c.Request.Context()

	var (
		mu         sync.Mutex
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
		resp.Data = dtochat.ChatWSResp{Flag: dtochat.WSFlagError}
		select {
		case <-connCtx.Done():
		case send <- &resp:
		}
	}

	for {
		var req dtochat.ChatWSReq
		if err := conn.ReadJSON(&req); err != nil {
			// 连接关闭 / 读错误：取消当前轮并终结写 goroutine
			mu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			mu.Unlock()
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

			dataChan := make(chan *dtochat.ChatSSEResp)
			go agentvc.Chat(turnCtx, dataChan, &dtochat.ChatReq{
				ConversationID: req.ConversationID,
				Messages:       req.Messages,
			})

			// 每轮转发 goroutine：dataChan → send，映射 flag
			go func() {
				defer func() {
					mu.Lock()
					busy = false
					turnCancel = nil
					mu.Unlock()
				}()

				first := true
				for v := range dataChan {
					resp := dtocode.SystemSuccess
					if v.Err != nil {
						// 错误帧统一规则：仅带 flag，不带 ChatSSEResp
						if turnCtx.Err() == context.Canceled {
							resp = dtocode.ChatWSCanceled
						} else {
							resp = dtocode.ChatSSEFailure
						}
						resp.Data = dtochat.ChatWSResp{Flag: dtochat.WSFlagError}
					} else {
						flag := dtochat.WSFlagDelta
						if first {
							flag = dtochat.WSFlagStart
						} else if v.Usage.TotalTokens > 0 {
							flag = dtochat.WSFlagDone
						}
						resp.Data = dtochat.ChatWSResp{Flag: flag, Data: *v}
					}
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
			if turnCancel != nil {
				turnCancel()
			}
			mu.Unlock()

		default:
			sendErr(dtocode.RequestParameterError)
		}
	}
}
