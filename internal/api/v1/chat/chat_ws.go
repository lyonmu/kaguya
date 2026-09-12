package chat

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"github.com/lyonmu/kaguya/pkg"
)

// chatWSWriteTimeout 限制单帧写出时长，客户端长时间不读取时断开连接，
// 避免写 goroutine 与生成轮次永久阻塞。
const chatWSWriteTimeout = 30 * time.Second

// chatWSRegistry 登记已建立的 WebSocket 连接。HTTP Server.Shutdown 不会关闭
// 已 hijack 的连接，所以关停时必须由应用主动取消并关闭它们。
var chatWSRegistry = struct {
	sync.Mutex
	stopping bool
	conns    map[*websocket.Conn]struct{}
}{conns: make(map[*websocket.Conn]struct{})}

// acquireChatWS 登记一条连接；服务已进入关停流程时拒绝新连接。
func acquireChatWS(conn *websocket.Conn) bool {
	chatWSRegistry.Lock()
	defer chatWSRegistry.Unlock()
	if chatWSRegistry.stopping {
		return false
	}
	chatWSRegistry.conns[conn] = struct{}{}
	return true
}

func releaseChatWS(conn *websocket.Conn) {
	chatWSRegistry.Lock()
	delete(chatWSRegistry.conns, conn)
	chatWSRegistry.Unlock()
}

// CloseAllChatWS 关闭全部 WebSocket 连接并拒绝后续连接。
// 关闭连接会打断读循环，由 handler 自身完成取消与资源释放。
func CloseAllChatWS() {
	chatWSRegistry.Lock()
	chatWSRegistry.stopping = true
	conns := make([]*websocket.Conn, 0, len(chatWSRegistry.conns))
	for conn := range chatWSRegistry.conns {
		conns = append(conns, conn)
	}
	chatWSRegistry.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// ChatWS
// @Tags      Chat
// @Summary   ChatWS
// @Description WebSocket 流式对话：上行 flag=chat/cancel，id 沿用会话雪花 ID，project_id 仅新对话使用，files 为项目内文件引用（最多 8 个，仅 SSE 同等的首轮引用行为）；下行 chat.flag=start/delta/done/error。delta 携带 block（text/reasoning/tool_call/tool_result），phase=start/delta/block_end；正文与思考的 block_end 不重复内容，唯一的整轮 done 仅携带 Usage
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

	connCtx, cancelConn := context.WithCancel(c.Request.Context())
	defer cancelConn()
	// 关停流程开始后不再接受新连接；已建立连接由 CloseAllChatWS 主动关闭。
	if !acquireChatWS(conn) {
		return
	}
	defer releaseChatWS(conn)

	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		busy       bool
		turnCancel context.CancelFunc
		turnConvID string
	)

	send := make(chan *dtocode.Response)

	// 唯一写 goroutine：串行写出所有下行帧
	go func() {
		for resp := range send {
			if err := conn.SetWriteDeadline(time.Now().Add(chatWSWriteTimeout)); err != nil {
				cancelConn()
				_ = conn.Close()
				return
			}
			if err := conn.WriteJSON(resp); err != nil {
				cancelConn() // 断连立即取消生成，防止未完成轮次落库
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
			cancelConn()
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
			turnConvID = req.ID
			mu.Unlock()

			dataChan := make(chan *dtochat.ChatResp)
			go agentvc.Chat(turnCtx, dataChan, &dtochat.ChatReq{
				ID:        req.ID,
				ModelID:   req.ModelID,
				Messages:  req.Messages,
				ProjectID: req.ProjectID,
				Files:     req.Files,
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

				for v := range dataChan {
					if v.Chat.ID != "" {
						mu.Lock()
						if turnConvID == "" {
							turnConvID = v.Chat.ID
						}
						mu.Unlock()
					}
					resp, _ := mapFrame(v)

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
			id := turnConvID
			if canceled {
				// 先标记用户主动停止，再取消上下文，落库才能区分 canceled 与断联。
				if id != "" {
					agentvc.StopConversation(id)
				}
				turnCancel()
				turnConvID = ""
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
// 生命周期由 Service 显式设置，不再根据 Token 数量推断结束帧。
// 返回的 bool 表示是否为错误帧；不向客户端暴露内部 error。
func mapFrame(v *dtochat.ChatResp) (dtocode.Response, bool) {
	resp := dtocode.SystemSuccess
	if v.Err != nil {
		resp = chatFailure(v.Err)
		chat := v.Chat
		chat.Flag = dtochat.WSFlagError
		resp.Data = dtochat.ChatResp{Chat: chat}
		return resp, true
	}
	resp.Data = v
	return resp, false
}

// chatFailure 只把服务层哨兵错误映射为固定的用户可读响应；
// 其余内部错误保持通用提示，细节由服务端日志保留。
func chatFailure(err error) dtocode.Response {
	switch {
	case errors.Is(err, serviceagent.ErrConversationBusy):
		return dtocode.ChatWSBusy
	case errors.Is(err, serviceagent.ErrConversationNotFound):
		return dtocode.ConversationNotFound
	case errors.Is(err, serviceagent.ErrChatModelNotConfigured):
		return dtocode.ChatModelNotConfigured
	case errors.Is(err, serviceagent.ErrChatConcurrencyLimited):
		return dtocode.ChatConcurrencyLimited
	case errors.Is(err, serviceagent.ErrProviderSecretUnavailable):
		return dtocode.ProviderSecretUnusable
	default:
		return dtocode.ChatSSEFailure
	}
}
