package conversation

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
)

func TestMemoryStore(t *testing.T) {
	tests := []struct {
		name            string
		ctx             context.Context
		id              string
		loadID          string
		append          []fantasy.Message
		want            int
		wantRole        fantasy.MessageRole
		wantContents    []string
		wantErr         error
		wantAppendErr   error
		verifyImmutable bool
	}{
		{
			name: "empty conversation",
			ctx:  context.Background(),
			id:   "a",
			want: 0,
		},
		{
			name:   "append user message",
			ctx:    context.Background(),
			id:     "a",
			append: []fantasy.Message{fantasy.NewUserMessage("hello")},
			want:   1,
		},
		{
			name: "append preserves order and content",
			ctx:  context.Background(),
			id:   "a",
			append: []fantasy.Message{
				fantasy.NewUserMessage("first"),
				fantasy.NewUserMessage("second"),
			},
			want:         2,
			wantRole:     fantasy.MessageRoleUser,
			wantContents: []string{"first", "second"},
		},
		{
			name:   "session isolation",
			ctx:    context.Background(),
			id:     "a",
			loadID: "b",
			append: []fantasy.Message{fantasy.NewUserMessage("hello")},
			want:   0,
		},
		{
			name:            "immutable load result",
			ctx:             context.Background(),
			id:              "a",
			append:          []fantasy.Message{fantasy.NewUserMessage("hello")},
			want:            1,
			verifyImmutable: true,
		},
		{
			name:          "empty conversation id",
			ctx:           context.Background(),
			id:            "",
			append:        []fantasy.Message{fantasy.NewUserMessage("hello")},
			wantErr:       errEmptyConversationID,
			wantAppendErr: errEmptyConversationID,
		},
		{
			name:    "cancelled",
			ctx:     cancelledContext(t),
			id:      "a",
			wantErr: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore()
			if tt.append != nil {
				err := store.Append(tt.ctx, tt.id, tt.append...)
				if tt.wantAppendErr != nil {
					if !errors.Is(err, tt.wantAppendErr) {
						t.Fatalf("Append error = %v, want %v", err, tt.wantAppendErr)
					}
				} else if err != nil {
					t.Fatalf("Append unexpected error: %v", err)
				}
			}
			loadID := tt.loadID
			if loadID == "" {
				loadID = tt.id
			}
			got, err := store.Load(tt.ctx, loadID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Load error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("Load returned %d messages, want %d", len(got), tt.want)
			}
			if tt.wantRole != "" {
				for i, msg := range got {
					if msg.Role != tt.wantRole {
						t.Fatalf("got[%d].Role = %q, want %q", i, msg.Role, tt.wantRole)
					}
				}
			}
			if tt.wantContents != nil {
				if len(got) != len(tt.wantContents) {
					t.Fatalf("got %d messages for content check, want %d", len(got), len(tt.wantContents))
				}
				for i, wantText := range tt.wantContents {
					gotText := messageText(t, got[i])
					if gotText != wantText {
						t.Fatalf("got[%d] text = %q, want %q", i, gotText, wantText)
					}
				}
			}
			if tt.verifyImmutable {
				got = append(got, fantasy.NewUserMessage("extra"))
				if len(got) > 0 {
					got[0].Content = append(got[0].Content, fantasy.TextPart{Text: "extra"})
				}
				got2, err := store.Load(tt.ctx, loadID)
				if err != nil {
					t.Fatalf("second Load unexpected error: %v", err)
				}
				if len(got2) != tt.want {
					t.Fatalf("after mutation, Load returned %d messages, want %d", len(got2), tt.want)
				}
				if len(got2[0].Content) != 1 {
					t.Fatalf("after mutation, message content has %d parts, want 1", len(got2[0].Content))
				}
			}
		})
	}
}

func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func messageText(t *testing.T, msg fantasy.Message) string {
	t.Helper()
	for _, part := range msg.Content {
		if tp, ok := part.(fantasy.TextPart); ok {
			return tp.Text
		}
	}
	t.Fatalf("message has no TextPart")
	return ""
}
