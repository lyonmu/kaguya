package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestConversationFailureCancellation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		canceled bool
		err      error
		ignored  bool
	}{
		{"client cancellation", true, context.Canceled, true},
		{"wrapped client cancellation", true, fmt.Errorf("query: %w", context.Canceled), true},
		{"internal cancellation", false, context.Canceled, false},
		{"database failure", false, errors.New("database unavailable"), false},
		{"database failure after disconnect", true, errors.New("database unavailable"), false},
		{"internal timeout", false, context.DeadlineExceeded, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.ErrorLevel)
			oldLogger := global.Logger
			global.Logger = zap.New(core)
			defer func() { global.Logger = oldLogger }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/conversation/page", nil).WithContext(ctx)

			conversationFailure(c, tt.err, dtocode.ConversationQueryFailure)

			if tt.ignored {
				if logs.Len() != 0 || c.Writer.Written() || w.Body.Len() != 0 {
					t.Fatalf("client cancellation logged or wrote response: logs=%d body=%s", logs.Len(), w.Body)
				}
			} else if logs.Len() != 1 || !c.Writer.Written() || w.Body.Len() == 0 {
				t.Fatalf("real failure was suppressed: logs=%d body=%s", logs.Len(), w.Body)
			}
		})
	}
}
