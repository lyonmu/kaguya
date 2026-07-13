package conversation

import (
	"context"
	"errors"
	"sync"

	"charm.land/fantasy"
)

var errEmptyConversationID = errors.New("conversation id is required")

var _ Store = (*MemoryStore)(nil)

// MemoryStore is a concurrent-safe in-memory implementation of Store.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string][]fantasy.Message
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string][]fantasy.Message)}
}

// Load returns a copy of all messages stored for conversationID.
// It returns a nil slice (length 0) when no messages have been appended.
func (m *MemoryStore) Load(ctx context.Context, conversationID string) ([]fantasy.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if conversationID == "" {
		return nil, errEmptyConversationID
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneMessages(m.items[conversationID]), nil
}

// Append stores copies of messages under conversationID, preserving order.
func (m *MemoryStore) Append(ctx context.Context, conversationID string, messages ...fantasy.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if conversationID == "" {
		return errEmptyConversationID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[conversationID] = append(m.items[conversationID], cloneMessages(messages)...)
	return nil
}

// cloneMessages returns an independent copy of src: a new message slice where
// each message's Content slice is also copied, so callers cannot mutate the
// internal state by modifying returned slices.
func cloneMessages(src []fantasy.Message) []fantasy.Message {
	if len(src) == 0 {
		return nil
	}
	dst := make([]fantasy.Message, len(src))
	for i, msg := range src {
		dst[i] = msg
		if len(msg.Content) > 0 {
			content := make([]fantasy.MessagePart, len(msg.Content))
			copy(content, msg.Content)
			dst[i].Content = content
		}
	}
	return dst
}
