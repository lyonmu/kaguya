package chat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestMapFramePreservesExplicitLifecycle(t *testing.T) {
	for _, flag := range []dtochat.WSFlag{dtochat.WSFlagStart, dtochat.WSFlagDelta, dtochat.WSFlagDone} {
		t.Run(string(flag), func(t *testing.T) {
			// done 在 usage=0 时也生效，且不携带重复内容。
			input := &dtochat.ChatResp{Chat: dtochat.Chat{ID: "conv", Flag: flag}}
			if flag == dtochat.WSFlagDelta {
				input.Chat.Block = &dtochat.ContentBlock{Type: dtochat.BlockTypeReasoning, Phase: dtochat.BlockPhaseEnd}
			}
			resp, isErr := mapFrame(input)
			if isErr {
				t.Fatal("unexpected error frame")
			}
			got, ok := resp.Data.(*dtochat.ChatResp)
			if !ok || got.Chat.Flag != flag || got.Chat.Block != input.Chat.Block {
				t.Fatalf("lost event: %+v", resp.Data)
			}
			data, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			if flag == dtochat.WSFlagDone {
				for _, field := range []string{`"turn_id"`, `"blocks"`, `"block"`, `"content"`} {
					if strings.Contains(string(data), field) {
						t.Fatalf("unexpected %s in done: %s", field, data)
					}
				}
			}
		})
	}
}

func TestMapFrameErrorPreservesIdentity(t *testing.T) {
	resp, isErr := mapFrame(&dtochat.ChatResp{Chat: dtochat.Chat{ID: "conv"}, Err: errors.New("private provider error")})
	if !isErr {
		t.Fatal("expected error")
	}
	got := resp.Data.(dtochat.ChatResp)
	if got.Chat.Flag != dtochat.WSFlagError || got.Chat.ID != "conv" {
		t.Fatalf("lost identity: %+v", got)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private provider error") {
		t.Fatalf("leaked error: %s", data)
	}
}
