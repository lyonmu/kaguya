package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

func TestReferenceIntentOnlyStoresBoundedTypedSelections(t *testing.T) {
	if intent, err := referenceIntent(nil); err != nil || intent != nil {
		t.Fatalf("empty intent: %+v err=%v", intent, err)
	}
	intent, err := referenceIntent([]dtochat.Reference{{Type: "directory", Path: "pkg", Name: strings.Repeat("display", 10000)}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"preparation_state":"requested","sources":[{"type":"directory","path":"pkg"}]}` {
		t.Fatalf("intent contains read proof or display data: %s", data)
	}
	var refs []dtochat.Reference
	for range 20 {
		refs = append(refs, dtochat.Reference{Type: "file", Path: strings.Repeat("<", 4096)})
	}
	if intent, err := referenceIntent(refs); !errors.Is(err, ErrContextBudgetExceeded) || intent != nil {
		t.Fatalf("escaped metadata must be bounded: intent=%v err=%v", intent, err)
	}
}

func TestChatFailedReferenceKeepsRequestedOrResolvedAudit(t *testing.T) {
	for _, scenario := range []string{"missing", "ignored", "provider_failure", "commit_failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, "project")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing" {
				if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("audit-reference-marker\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "ignored" {
				if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("note.txt\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			project, err := client.KaguyaProject.Create().SetName("reference-audit").SetPath(dir).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetChatMaxRetries(0).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if scenario != "commit_failure" {
					http.Error(w, "fixture provider failure", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var savedRequested atomic.Bool
			client.KaguyaChatTurn.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if scenario == "commit_failure" {
						if change, ok := mutation.(*ent.KaguyaChatTurnMutation); ok {
							if status, exists := change.Status(); exists && status == kaguyachatturn.StatusCompleted {
								return nil, errors.New("fixture completed transaction failure")
							}
						}
					}
					value, err := next.Mutate(ctx, mutation)
					if err != nil || mutation.Op() != ent.OpCreate {
						return value, err
					}
					created, ok := value.(*ent.KaguyaChatTurn)
					if !ok {
						return nil, errors.New("fixture expected created turn")
					}
					persisted, err := client.KaguyaChatTurn.Get(ctx, created.ID)
					if err != nil {
						return nil, err
					}
					data, err := json.Marshal(persisted.ReferenceSummary)
					if err != nil || persisted.Status != kaguyachatturn.StatusRunning || string(data) != `{"preparation_state":"requested","sources":[{"type":"file","path":"note.txt"}]}` {
						return nil, errors.New("requested intent must be persisted before parsing")
					}
					savedRequested.Store(true)
					return value, nil
				})
			})
			frames := make(chan *dtochat.ChatResp)
			go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{ProjectID: project.ID, ModelID: model.ID, Messages: "inspect my selected file", References: []dtochat.Reference{{Type: "file", Path: "note.txt"}}})
			var failed *dtochat.ChatResp
			for frame := range frames {
				if frame.Chat.Flag == dtochat.ChatFlagDone {
					t.Error("failure must not emit done")
				}
				if frame.Err != nil {
					failed = frame
				}
			}
			if !savedRequested.Load() || failed == nil || failed.ReferenceSummary == nil {
				t.Fatalf("missing requested/failure audit: saved=%v frame=%+v", savedRequested.Load(), failed)
			}
			rows, err := (&AgentSvc{}).ConversationTurns(ctx, failed.Chat.ID, &dtochat.TurnPageReq{Limit: 5})
			if err != nil || len(rows.Items) != 1 || rows.Items[0].Status != "failed" || rows.Items[0].UserContent != "inspect my selected file" {
				t.Fatalf("failed history mismatch: %+v err=%v", rows, err)
			}
			history, err := json.Marshal(rows.Items[0].ReferenceSummary)
			if err != nil {
				t.Fatal(err)
			}
			audit, err := json.Marshal(failed.ReferenceSummary)
			if err != nil || string(history) != string(audit) {
				t.Fatalf("error/history mismatch: error=%s history=%s err=%v", audit, history, err)
			}
			state, wantCalls := "requested", int32(0)
			if scenario == "provider_failure" || scenario == "commit_failure" {
				state, wantCalls = "resolved", 1
				files := failed.ReferenceSummary.Sources[0].Files
				if len(files) != 1 || !files[0].Included || len(files[0].Hash) != 64 || len(files[0].Ranges) != 1 {
					t.Fatalf("resolved failure lost actual content proof: %+v", files)
				}
			}
			if failed.ReferenceSummary.PreparationState != state || calls.Load() != wantCalls {
				t.Fatalf("state=%s calls=%d want=%s/%d", failed.ReferenceSummary.PreparationState, calls.Load(), state, wantCalls)
			}
		})
	}
}
