package agent

import (
	"context"
	"errors"
	"sync"
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
