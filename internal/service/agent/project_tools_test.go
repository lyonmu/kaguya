package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

func TestProjectToolWorkspaceAuthority(t *testing.T) {
	ctx, _ := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "project", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	svc := &AgentSvc{}
	set, err := svc.projectTools(ctx, "new", "", 0)
	if err != nil || set != nil {
		t.Fatalf("ordinary chat has tools: %v", err)
	}
	turn := testCompletedTurn("123", 0)
	turn.ProjectID = p.ID
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	set, err = svc.projectTools(ctx, "123", "forged-project", 1)
	if err != nil {
		t.Fatal(err)
	}
	if set.CWD() != home || len(set.CodingTools()) != 4 {
		t.Fatal("wrong workspace")
	}
	set.Close()
	if err := (&projectsvc.ProjectSvc{}).Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	set, err = svc.projectTools(ctx, "123", p.ID, 1)
	if err != nil || set != nil {
		t.Fatalf("deleted project regained tool access: %v", err)
	}
	if _, err := svc.projectTools(ctx, "new", p.ID, 0); err == nil {
		t.Fatal("deleted project accepted for new chat")
	}
}
func TestChatExecutesCodingToolAndPersistsResult(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "project", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		names := []string{}
		for _, tool := range body.Tools {
			names = append(names, tool.Function.Name)
		}
		if !reflect.DeepEqual(names, []string{"read", "bash", "edit", "write"}) {
			t.Errorf("tools=%v", names)
		}
		if !strings.Contains(string(body.Messages), home) {
			t.Error("missing workspace prompt")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, reason any) {
			chunk := map[string]any{"id": "test", "object": "chat.completion.chunk", "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if requests.Add(1) == 1 {
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "write-file", "type": "function", "function": map[string]any{"name": "write", "arguments": `{"path":"hello.txt","content":"hello from tool"}`}}}}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			if !strings.Contains(string(body.Messages), "Successfully wrote") {
				t.Error("tool result not returned to model")
			}
			emit(map[string]any{"role": "assistant", "content": "done"}, nil)
			emit(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{ProjectID: p.ID, ModelID: model.ID, Messages: "create hello.txt"})
	done, toolBlocks := 0, 0
	id := ""
	for frame := range frames {
		if frame.Err != nil {
			t.Fatal(frame.Err)
		}
		id = frame.Chat.ID
		if frame.Chat.Flag == dtochat.WSFlagDone {
			done++
		}
		if frame.Chat.Block != nil && frame.Chat.Block.Type == dtochat.BlockTypeToolCall {
			toolBlocks++
		}
	}
	if done != 1 || requests.Load() != 2 || toolBlocks == 0 {
		t.Fatalf("done=%d requests=%d tools=%d", done, requests.Load(), toolBlocks)
	}
	b, err := os.ReadFile(filepath.Join(home, "hello.txt"))
	if err != nil || string(b) != "hello from tool" {
		t.Fatalf("file=%q err=%v", b, err)
	}
	detail, err := (&AgentSvc{}).ConversationDetail(ctx, id)
	if err != nil || detail.ToolCalls != 1 || detail.ProjectID == nil || *detail.ProjectID != p.ID {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	turns, err := (&AgentSvc{}).ConversationTurns(ctx, id, &dtochat.TurnPageReq{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, block := range turns.Items[0].Blocks {
		if block.ToolName == "write" && block.Output != nil && strings.Contains(block.Output.Text, "Successfully wrote") {
			found = true
		}
	}
	if !found {
		t.Fatal("tool output not persisted")
	}
}
