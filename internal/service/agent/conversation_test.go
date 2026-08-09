package agent

import (
	"testing"

	"charm.land/fantasy"
)

func TestConversationStoreHistory_UnknownSession(t *testing.T) {
	s := newConversationStore()
	if got := s.history("not-exist"); got != nil {
		t.Fatalf("未知会话应返回 nil，got %v", got)
	}
}

func TestConversationStoreAppendAndHistory(t *testing.T) {
	s := newConversationStore()
	s.append("c1", fantasy.NewUserMessage("你好"))
	s.append("c1", fantasy.NewUserMessage("再问一次"))

	history := s.history("c1")
	if len(history) != 2 {
		t.Fatalf("历史长度应为 2，got %d", len(history))
	}
	if history[0].Role != fantasy.MessageRoleUser {
		t.Fatalf("第一条角色应为 user，got %q", history[0].Role)
	}

	// 返回的是拷贝：修改返回值不应影响内部状态
	history[0].Role = fantasy.MessageRoleAssistant
	if got := s.history("c1"); got[0].Role != fantasy.MessageRoleUser {
		t.Fatalf("外部修改不应影响内部状态")
	}
}

func TestConversationStoreConcurrentAppend(t *testing.T) {
	s := newConversationStore()
	const n = 100
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			s.append("c1", fantasy.NewUserMessage("并发消息"))
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if got := len(s.history("c1")); got != n {
		t.Fatalf("并发追加后历史长度应为 %d，got %d", n, got)
	}
}
