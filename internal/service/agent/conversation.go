package agent

import (
	"sync"

	"charm.land/fantasy"
)

// conversation 一个会话的完整消息历史。
type conversation struct {
	mu       sync.Mutex
	messages []fantasy.Message
}

// conversationStore 内存会话历史存储（不落库，重启即丢）。
type conversationStore struct {
	mu    sync.RWMutex
	items map[string]*conversation
}

// conversationStoreInstance 全链路共享的会话存储单例。
var conversationStoreInstance = newConversationStore()

func newConversationStore() *conversationStore {
	return &conversationStore{items: make(map[string]*conversation)}
}

// history 返回会话当前历史（拷贝），未知会话返回 nil。
func (s *conversationStore) history(convID string) []fantasy.Message {
	s.mu.RLock()
	conv, ok := s.items[convID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	conv.mu.Lock()
	defer conv.mu.Unlock()
	out := make([]fantasy.Message, len(conv.messages))
	copy(out, conv.messages)
	return out
}

// append 向会话追加消息，会话不存在时自动创建。
func (s *conversationStore) append(convID string, msgs ...fantasy.Message) {
	s.mu.RLock()
	conv, ok := s.items[convID]
	s.mu.RUnlock()
	if !ok {
		s.mu.Lock()
		conv = s.items[convID]
		if conv == nil {
			conv = &conversation{}
			s.items[convID] = conv
		}
		s.mu.Unlock()
	}

	conv.mu.Lock()
	conv.messages = append(conv.messages, msgs...)
	conv.mu.Unlock()
}
