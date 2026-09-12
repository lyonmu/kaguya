package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	// 单实例服务同时进行的模型生成轮次上限；超出时快速失败，避免无界占用
	// 上游额度、连接和项目工具工作区。
	maxConcurrentChats = 16
	// 标题生成属于后台辅助任务，限制并发避免与聊天争用数据库和模型调用。
	maxConcurrentTitleTasks = 2
)

// ErrChatConcurrencyLimited 表示同时进行的聊天轮次已达上限。
var ErrChatConcurrencyLimited = errors.New("too many concurrent chat turns")

// conversationLease 是一次进行中轮次的租约。userStop 记录用户主动停止，
// 用于在取消时区分“正常停止”（canceled）与断联/超时（interrupted）。
type conversationLease struct{ userStop atomic.Bool }

// 同一进程跨 SSE/WS 连接也不能同时生成同一会话；数据库版本条件再防多实例覆盖。
var activeConversations = struct {
	sync.Mutex
	leases map[string]*conversationLease
}{leases: make(map[string]*conversationLease)}

func acquireConversation(id string) (*conversationLease, func(), error) {
	activeConversations.Lock()
	defer activeConversations.Unlock()
	if activeConversations.leases[id] != nil {
		return nil, nil, ErrConversationBusy
	}
	lease := &conversationLease{}
	activeConversations.leases[id] = lease
	return lease, func() {
		activeConversations.Lock()
		delete(activeConversations.leases, id)
		activeConversations.Unlock()
	}, nil
}

// StopConversation 记录用户主动停止本轮生成；没有运行轮次时是幂等空操作。
// 取消本身仍由连接上下文完成，这里只标记取消原因。
func (s *AgentSvc) StopConversation(id string) {
	activeConversations.Lock()
	lease := activeConversations.leases[id]
	activeConversations.Unlock()
	if lease != nil {
		lease.userStop.Store(true)
	}
}

var (
	chatSlots  = make(chan struct{}, maxConcurrentChats)
	titleSlots = make(chan struct{}, maxConcurrentTitleTasks)
)

// tryAcquire 立即获取一个槽位，不等待；满员时返回 false。
func tryAcquire(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseSlot(slots chan struct{}) { <-slots }

// activeWork 统计进行中的聊天轮次与标题任务；关停时等待其结束，避免数据库
// 关闭后仍有写入。
var activeWork sync.WaitGroup

func beginWork() func() {
	activeWork.Add(1)
	return activeWork.Done
}

// WaitActive 等待进行中的工作结束或 ctx 超时。
func WaitActive(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		activeWork.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	shutdownOnce               sync.Once
	titleTaskCtx, endTitleTask = context.WithCancel(context.Background())
)

// Shutdown 取消后台标题任务；聊天轮次由各自的请求 context 控制。
func Shutdown() { shutdownOnce.Do(endTitleTask) }
