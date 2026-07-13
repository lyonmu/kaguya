package conversation

import (
	"context"

	"charm.land/fantasy"
)

// Store defines the conversation persistence contract.
// Implementations must be safe for concurrent use.
type Store interface {
	Load(ctx context.Context, conversationID string) ([]fantasy.Message, error)
	Append(ctx context.Context, conversationID string, messages ...fantasy.Message) error
}
