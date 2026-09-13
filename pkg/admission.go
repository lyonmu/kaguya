package pkg

import (
	"context"
	"sync"
)

// Admission 记录正在处理的请求，供关停流程先停止准入、再等待已登记请求返回。
// Web 与 Desktop 两种启动模式共用，调用方保证 Stop 先于 Wait。
type Admission struct {
	mu       sync.Mutex
	stopping bool
	inflight sync.WaitGroup
}

func NewAdmission() *Admission { return &Admission{} }

// Enter 登记一个请求。Stop 之后返回 false，表示该请求不应继续处理。
func (a *Admission) Enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return false
	}
	a.inflight.Add(1)
	return true
}

// Leave 注销一个已登记的请求，必须与成功的 Enter 成对出现。
func (a *Admission) Leave() { a.inflight.Done() }

// Stop 关闭准入；等待在途请求是独立的 Wait 调用，避免与新的 Enter 交叉。
func (a *Admission) Stop() {
	a.mu.Lock()
	a.stopping = true
	a.mu.Unlock()
}

// Wait 等待全部已登记请求返回，或 ctx 先结束。
func (a *Admission) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		a.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
