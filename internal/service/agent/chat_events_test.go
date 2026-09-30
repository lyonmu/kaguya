package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestTitlePublisherDeliversToAllSubscribers(t *testing.T) {
	publisher := NewTitlePublisher()
	defer publisher.Close()
	first, unsubscribeFirst, err := publisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFirst()
	second, unsubscribeSecond, err := publisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSecond()

	event := dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: "c1", Title: "标题", Revision: 2}
	publisher.Publish(event)
	for name, subscription := range map[string]*TitleSubscription{"first": first, "second": second} {
		select {
		case got := <-subscription.Events:
			if got != event {
				t.Fatalf("%s received %+v", name, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive the event", name)
		}
	}
}

func TestTitlePublisherDropsSlowSubscriber(t *testing.T) {
	publisher := NewTitlePublisher()
	defer publisher.Close()
	subscription, unsubscribe, err := publisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	event := dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: "c1"}
	for index := 0; index <= titleSubscriberBuffer; index++ {
		publisher.Publish(event)
	}
	// 慢订阅者被注销后 channel 关闭，读取剩余缓冲最终会得到关闭信号。
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-subscription.Events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("overflowed subscriber was not dropped")
		}
	}
}

func TestTitlePublisherUnsubscribeAndCloseAreIdempotent(t *testing.T) {
	publisher := NewTitlePublisher()
	subscription, unsubscribe, err := publisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	unsubscribe()
	if _, open := <-subscription.Events; open {
		t.Fatal("unsubscribe did not close the subscriber channel")
	}
	publisher.Close()
	publisher.Close()
	if _, _, err := publisher.Subscribe(); err == nil {
		t.Fatal("subscribe after close must fail")
	}
}

func TestTitlePublisherConcurrentLifecycle(t *testing.T) {
	publisher := NewTitlePublisher()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 50; index++ {
				subscription, unsubscribe, err := publisher.Subscribe()
				if err != nil {
					return
				}
				publisher.Publish(dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: "c"})
				if subscription.Events != nil {
					select {
					case <-subscription.Events:
					default:
					}
				}
				unsubscribe()
			}
		}()
	}
	wait.Wait()
	publisher.Close()
}

func TestTurnEmitterSerializesSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *dtochat.ChatResp, 64)
	emitter := newTurnEmitter(ctx, out)

	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 4; index++ {
				if !emitter.Emit(&dtochat.ChatResp{}) {
					return
				}
			}
		}()
	}
	wait.Wait()
	close(out)
	seen := make(map[int64]bool)
	count := 0
	for resp := range out {
		if resp.Seq <= 0 || seen[resp.Seq] {
			t.Fatalf("invalid sequence %d", resp.Seq)
		}
		seen[resp.Seq] = true
		count++
	}
	if count != 32 {
		t.Fatalf("emitted %d frames", count)
	}
}

func TestTurnEmitterStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	emitter := newTurnEmitter(ctx, make(chan *dtochat.ChatResp))
	cancel()
	if emitter.Emit(&dtochat.ChatResp{}) {
		t.Fatal("emit succeeded after cancellation")
	}
}
