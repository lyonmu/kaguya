package agent

import (
	"context"
	"sync"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// titleSubscriberBuffer 是每个事件订阅者的有界缓冲；慢订阅者积压超过该值即断开，
// 由前端重连后重新读取数据库快照，DB 始终是标题的权威来源。
const titleSubscriberBuffer = 64

// TitleSubscription 是一次事件订阅；Events 关闭表示订阅已被移除或发布者关闭。
type TitleSubscription struct {
	Events <-chan dtochat.TitleEvent
}

// TitlePublisher 只发布标题事件。订阅 registry 是 channel 的唯一关闭者，
// Publish 在锁外发送，慢订阅者不会阻塞模型调用或数据库提交。
type TitlePublisher struct {
	mu     sync.Mutex
	subs   map[*titleSubscriber]struct{}
	closed bool
}

type titleSubscriber struct {
	ch chan dtochat.TitleEvent
}

// NewTitlePublisher 创建发布者；生命周期由 appRuntime 持有，root context 取消时 Close。
func NewTitlePublisher() *TitlePublisher {
	return &TitlePublisher{subs: make(map[*titleSubscriber]struct{})}
}

// Subscribe 注册一个订阅者。发布者已关闭时返回错误，调用方应回退为普通错误响应。
func (p *TitlePublisher) Subscribe() (*TitleSubscription, func(), error) {
	if p == nil {
		return nil, func() {}, context.Canceled
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, func() {}, context.Canceled
	}
	sub := &titleSubscriber{ch: make(chan dtochat.TitleEvent, titleSubscriberBuffer)}
	p.subs[sub] = struct{}{}
	var once sync.Once
	unsubscribe := func() { once.Do(func() { p.remove(sub) }) }
	return &TitleSubscription{Events: sub.ch}, unsubscribe, nil
}

// Publish 向所有订阅者非阻塞投递；缓冲满的订阅者被移除并关闭，
// 客户端重连后通过会话列表/detail 补齐。
//
// 非阻塞发送与关闭必须在同一把锁内完成：否则并发 Unsubscribe/Close 会在
// 发送与 close 之间产生竞态（发送到已关闭 channel 会 panic）。
func (p *TitlePublisher) Publish(event dtochat.TitleEvent) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for sub := range p.subs {
		select {
		case sub.ch <- event:
		default:
			delete(p.subs, sub)
			close(sub.ch)
		}
	}
}

// remove 幂等地注销订阅者并关闭其 channel。
func (p *TitlePublisher) remove(sub *titleSubscriber) {
	p.mu.Lock()
	if _, ok := p.subs[sub]; !ok {
		p.mu.Unlock()
		return
	}
	delete(p.subs, sub)
	p.mu.Unlock()
	close(sub.ch)
}

// Close 关闭发布者并注销全部订阅者；重复调用安全。
func (p *TitlePublisher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	subscribers := make([]*titleSubscriber, 0, len(p.subs))
	for sub := range p.subs {
		subscribers = append(subscribers, sub)
	}
	p.subs = make(map[*titleSubscriber]struct{})
	p.mu.Unlock()
	for _, sub := range subscribers {
		close(sub.ch)
	}
}

// DefaultTitlePublisher 是进程内默认发布者，由 appRuntime 在启动时替换为
// 绑定 root context 的实例；测试可直接使用零值（方法对 nil 安全）。
var DefaultTitlePublisher *TitlePublisher = NewTitlePublisher()
