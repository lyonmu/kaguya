package chat

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

func TestMapFrameKnownFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want dtocode.Response
	}{
		{"busy", serviceagent.ErrConversationBusy, dtocode.ChatBusy},
		{"not found", serviceagent.ErrConversationNotFound, dtocode.ConversationNotFound},
		{"model", serviceagent.ErrChatModelNotConfigured, dtocode.ChatModelNotConfigured},
		{"concurrency", serviceagent.ErrChatConcurrencyLimited, dtocode.ChatConcurrencyLimited},
		{"provider secret", serviceagent.ErrProviderSecretUnavailable, dtocode.ProviderSecretUnusable},
		{"unknown", errors.New("provider failed with secret"), dtocode.ChatSSEFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, isErr := mapFrame(&dtochat.ChatResp{Err: tt.err, Chat: dtochat.Chat{ID: "1"}})
			if !isErr {
				t.Fatal("expected error frame")
			}
			if resp.Code != tt.want.Code || resp.Message != tt.want.Message {
				t.Fatalf("resp=%+v want=%+v", resp, tt.want)
			}
			data, ok := resp.Data.(dtochat.ChatResp)
			if !ok || data.Chat.Flag != dtochat.ChatFlagError || data.Err != nil {
				t.Fatalf("error frame leaked internal error: %+v", resp.Data)
			}
		})
	}
}

func TestMapFrameSuccessKeepsData(t *testing.T) {
	frame := &dtochat.ChatResp{Chat: dtochat.Chat{ID: "1", Flag: dtochat.ChatFlagDone}}
	resp, isErr := mapFrame(frame)
	if isErr || resp.Code != dtocode.SystemSuccess.Code {
		t.Fatalf("resp=%+v isErr=%v", resp, isErr)
	}
	if resp.Data != frame {
		t.Fatalf("success frame data changed: %+v", resp.Data)
	}
}

// httptest.ResponseRecorder 不支持写入截止时间，但真实 HTTP 服务器支持；
// 不支持时不能中断流。
func TestRefreshSSEWriteDeadlineToleratesUnsupportedWriter(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if !refreshSSEWriteDeadline(c) {
		t.Fatal("unsupported writer must not abort the stream")
	}
}
