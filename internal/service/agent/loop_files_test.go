package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
	dto "github.com/lyonmu/kaguya/internal/dto/chat"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

func TestLoopUnlimitedAndResumableLimit(t *testing.T) {
	for _, limit := range []int{0, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, client := setupChatTest(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("reference evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			project, err := client.KaguyaProject.Create().SetName("test").SetPath(home).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			wanted := 66
			if limit > 0 {
				wanted = 3
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !strings.Contains(string(body.Messages), "reference evidence") {
					t.Error("reference contents missing from model context")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				delta := map[string]any{"role": "assistant", "content": "done"}
				reason := "stop"
				if calls < wanted {
					delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("read-%d", calls), "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"note.txt"}`}}}}
					reason = "tool_calls"
				}
				chunk := map[string]any{"id": "test", "object": "chat.completion.chunk", "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
				b, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", b)
				fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
			}))
			defer server.Close()
			provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetBaseURL(server.URL).SetAPIKey("test").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetAgentMaxSteps(limit).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			run := func(req *dto.ChatReq) *dto.ChatResp {
				t.Helper()
				ch := make(chan *dto.ChatResp)
				go (&AgentSvc{}).Chat(ctx, ch, req)
				var done *dto.ChatResp
				for frame := range ch {
					if frame.Err != nil {
						t.Fatal(frame.Err)
					}
					if frame.Chat.Flag == dto.WSFlagDone {
						done = frame
					}
				}
				if done == nil {
					t.Fatal("missing persisted completion")
				}
				return done
			}
			first := run(&dto.ChatReq{ProjectID: project.ID, ModelID: model.ID, Messages: `inspect @"note.txt"`, Files: []string{"note.txt"}})
			if limit == 0 {
				if calls != 66 || first.FinishReason != "stop" {
					t.Fatalf("default stopped early: %d %+v", calls, first)
				}
				return
			}
			if calls != 2 || first.FinishReason != "step_limit" {
				t.Fatalf("not paused: %d %+v", calls, first)
			}
			messages, version, err := loadConversation(ctx, first.Chat.ID)
			if err != nil || version != 1 || messages[len(messages)-1].Role != fantasy.MessageRoleTool {
				t.Fatalf("checkpoint lost tool results: %v", err)
			}
			second := run(&dto.ChatReq{ID: first.Chat.ID, ProjectID: "untrusted-project", ModelID: model.ID, Messages: "continue"})
			if calls != 3 || second.FinishReason != "stop" {
				t.Fatalf("resume failed: %d %+v", calls, second)
			}
		})
	}
}

func TestProjectFileSearchAndReferenceBoundary(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{"src/main.go", "space file.txt", "node_modules/hidden.txt", ".git/config", ".env", "src/.generated/output.go"} {
		full := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("hello"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	p, err := client.KaguyaProject.Create().SetName("test").SetPath(home).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := (&projectsvc.ProjectSvc{}).Files(ctx, p.ID, "")
	if err != nil || len(files.Files) != 2 {
		t.Fatalf("files=%+v %v", files, err)
	}
	files, err = (&projectsvc.ProjectSvc{}).Files(ctx, p.ID, "MAIN")
	if err != nil || len(files.Files) != 1 || files.Files[0] != "src/main.go" {
		t.Fatalf("search=%+v %v", files, err)
	}
	set, err := (&AgentSvc{}).projectTools(ctx, "new", p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	for _, path := range []string{"../secret", "escape", filepath.Join(outside, "secret")} {
		if _, err := set.ReadReference(ctx, path); err == nil {
			t.Fatalf("escaped reference: %s", path)
		}
	}
	if got, err := set.ReadReference(ctx, "space file.txt"); err != nil || !strings.Contains(got, "hello") {
		t.Fatalf("reference=%q %v", got, err)
	}
}
