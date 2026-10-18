package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpSecretInput    = `{"api_key":"fake-mcp-input-key","nested":[{"Authorization":"fake-mcp-input-auth","client_secret":"fake-mcp-input-secret"}],"note":"keep-this-note"}`
	mcpSecretHeader   = "fake-mcp-configured-header"
	mcpSecretEnv      = "fake-mcp-configured-env"
	mcpSecretResult   = "fake-user-visible-tool-result"
	mcpSecretRPCError = "fake-mcp-protocol-error-body"
	mcpSecretProvider = "fake-mcp-provider-key"
)

func secretBoundaryMCPServer(markerDir, outcome string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "secret-boundary", Version: "1"}, &sdk.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	server.AddTool(&sdk.Tool{
		Name: "boundary", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{
			"type": "object", "required": []string{"api_key", "nested", "note"},
			"properties": map[string]any{
				"api_key": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"},
				"nested": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"Authorization", "client_secret"}, "properties": map[string]any{"Authorization": map[string]any{"type": "string"}, "client_secret": map[string]any{"type": "string"}}}},
			},
		},
	}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls, err := os.OpenFile(filepath.Join(markerDir, "calls.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, callErr := calls.WriteString("called\n")
		if err := errors.Join(callErr, calls.Close()); err != nil {
			return nil, err
		}
		var actual, expected any
		if err := json.Unmarshal(request.Params.Arguments, &actual); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(mcpSecretInput), &expected)
		if !reflect.DeepEqual(actual, expected) {
			return nil, errors.New("display redaction changed remote execution input")
		}
		file, err := os.OpenFile(filepath.Join(markerDir, "executed.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		} // A replay cannot silently overwrite its first effect.
		_, writeErr := file.Write(request.Params.Arguments)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return nil, err
		}
		if outcome == "protocol_error" {
			return nil, errors.New(mcpSecretRPCError + " " + mcpSecretHeader + " " + mcpSecretEnv)
		}
		return &sdk.CallToolResult{IsError: outcome == "tool_error", Content: []sdk.Content{&sdk.TextContent{Text: mcpSecretResult}}}, nil
	})
	return server
}

// All three transports carry original input to a real SDK server after an HTTP
// approval; only the approval display is redacted. Protocol causes stay private,
// while a valid tool body (including IsError bodies) remains a user-visible result.
func TestMCPHTTPApprovalSecretBoundaries(t *testing.T) {
	for _, transport := range []string{"stdio", "streamable-http", "sse"} {
		for _, outcome := range []string{"success", "tool_error", "protocol_error"} {
			t.Run(transport+"/"+outcome, func(t *testing.T) {
				f := setupSecretChat(t)
				markerDir := t.TempDir()
				manager := agentmcp.NewManager()
				oldManager := agentmcp.Default
				agentmcp.Default = manager
				t.Cleanup(func() { manager.Close(); agentmcp.Default = oldManager })
				config := agentmcp.Config{Name: "secret-boundary", Transport: transport, TimeoutSeconds: 3}
				var authenticated atomic.Int64
				if transport == "stdio" {
					config.Command, config.WorkingDirectory = os.Args[0], markerDir
					config.Args = []string{"-test.run=^TestMCPSecretBoundaryHelperProcess$"}
					config.Env = map[string]string{"KAGUYA_MCP_SECRET_HELPER": "1", "KAGUYA_MCP_SECRET_OUTCOME": outcome, "KAGUYA_MCP_FAKE_SECRET": mcpSecretEnv}
				} else {
					server := secretBoundaryMCPServer(markerDir, outcome)
					var handler http.Handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
					if transport == "sse" {
						handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
					}
					remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer "+mcpSecretHeader {
							t.Error("MCP did not receive original authentication header")
							http.Error(w, "unauthorized", http.StatusUnauthorized)
							return
						}
						authenticated.Add(1)
						handler.ServeHTTP(w, r)
					}))
					t.Cleanup(remote.Close)
					config.URL, config.Headers = remote.URL, map[string]string{"Authorization": "Bearer " + mcpSecretHeader}
				}
				connection, err := agentmcp.PrepareWithLifetime(f.ctx, f.ctx, config)
				if err != nil {
					t.Fatal(err)
				}
				manager.Replace("secret-server", connection)
				registered := manager.Tools()
				if len(registered) != 1 {
					t.Fatal("real MCP adapter was not registered")
				}
				toolName := registered[0].Info().Name
				var modelRequests atomic.Int64
				modelHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					delta, reason := map[string]any{"role": "assistant", "content": "done"}, "stop"
					if modelRequests.Add(1) == 1 {
						delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "mcp-secret-call", "type": "function", "function": map[string]any{"name": toolName, "arguments": mcpSecretInput}}}}
						reason = "tool_calls"
					} else {
						body, _ := io.ReadAll(r.Body)
						if outcome == "protocol_error" {
							if strings.Contains(string(body), mcpSecretRPCError) || strings.Contains(string(body), mcpSecretHeader) || strings.Contains(string(body), mcpSecretEnv) {
								t.Error("protocol cause crossed follow-up model boundary")
							}
						} else if !strings.Contains(string(body), mcpSecretResult) {
							t.Error("valid tool body was removed from model context")
						}
					}
					for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": reason}} {
						chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}})
						fmt.Fprintf(w, "data: %s\n\n", chunk)
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				}))
				t.Cleanup(modelHost.Close)
				model := f.model(t, modelHost.URL, mcpSecretProvider)
				payload, _ := json.Marshal(dtochat.ChatReq{Messages: "call the MCP fixture", ModelID: model, PermissionMode: "full_approval"})
				response := f.request(t, http.MethodPost, "/v1/chat/sse", string(payload))
				defer response.Body.Close()
				scanner := bufio.NewScanner(response.Body)
				scanner.Buffer(make([]byte, 4096), 1<<20)
				var pending *dtochat.Approval
				var conversation string
				done, results := 0, 0
				for scanner.Scan() {
					line := scanner.Text()
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var frame struct {
						Code int              `json:"code"`
						Data dtochat.ChatResp `json:"data"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil || frame.Code != dtocode.SystemSuccess.Code {
						t.Fatal("invalid MCP secret-boundary SSE frame", err)
					}
					for _, secret := range []string{mcpSecretProvider, mcpSecretHeader, mcpSecretEnv, mcpSecretRPCError} {
						if strings.Contains(line, secret) {
							t.Fatal("configuration/protocol secret crossed SSE boundary")
						}
					}
					if frame.Data.Chat.Flag == dtochat.ChatFlagDone {
						done++
					}
					if block := frame.Data.Chat.Block; block != nil && block.Type == dtochat.BlockTypeToolResult {
						results++
						if block.IsError != (outcome != "success") || block.Output == nil {
							t.Fatal("MCP result error status was changed")
						}
						if outcome != "protocol_error" && !strings.Contains(block.Output.Text, mcpSecretResult) {
							t.Fatal("valid tool body was removed from SSE result")
						}
					}
					if frame.Data.Approval == nil || frame.Data.Approval.Status != "pending" {
						continue
					}
					if pending != nil {
						t.Fatal("MCP tool was replayed")
					}
					pending, conversation = frame.Data.Approval, frame.Data.Chat.ID
					if pending.Effect != "unknown" || !strings.HasPrefix(pending.ToolIdentity, "mcp:secret-server:boundary:") {
						t.Fatal("remote hint bypassed trustworthy classification")
					}
					requireMCPDisplayCopy(t, pending.Input)
					if _, err := os.Stat(filepath.Join(markerDir, "executed.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("remote side effect happened before approval", err)
					}
					if _, err := os.Stat(filepath.Join(markerDir, "calls.log")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("remote tool was called before approval", err)
					}
					basePath := "/v1/chat/conversation/" + conversation + "/approvals"
					snapshot := f.request(t, http.MethodGet, basePath, "")
					var listing struct {
						Code int                `json:"code"`
						Data []dtochat.Approval `json:"data"`
					}
					decodeErr := json.NewDecoder(snapshot.Body).Decode(&listing)
					snapshot.Body.Close()
					if decodeErr != nil || listing.Code != dtocode.SystemSuccess.Code || len(listing.Data) != 1 || listing.Data[0].ApprovalID != pending.ApprovalID {
						t.Fatal("pending snapshot lost its live approval", decodeErr)
					}
					requireMCPDisplayCopy(t, listing.Data[0].Input)
					row, err := f.client.KaguyaApproval.Query().Only(f.ctx)
					if err != nil || row.InputHash != permission.HashInput(mcpSecretInput) || row.ToolIdentity != pending.ToolIdentity {
						t.Fatal("display redaction changed persisted authorization", err)
					}
					approved := f.request(t, http.MethodPost, basePath+"/"+pending.ApprovalID, `{"decision":"approve"}`)
					var envelope struct {
						Code int `json:"code"`
					}
					decodeErr = json.NewDecoder(approved.Body).Decode(&envelope)
					approved.Body.Close()
					if decodeErr != nil || envelope.Code != dtocode.SystemSuccess.Code {
						t.Fatal("MCP approval did not commit", decodeErr)
					}
				}
				if err := scanner.Err(); err != nil || pending == nil || done != 1 || results != 1 || modelRequests.Load() != 2 {
					t.Fatal("MCP secret fixture did not complete once", err, done, results)
				}
				waitSecretChatOwners(t)
				actual, err := os.ReadFile(filepath.Join(markerDir, "executed.json"))
				if err != nil {
					t.Fatal("authorized remote marker missing", err)
				}
				var got, want any
				_ = json.Unmarshal(actual, &got)
				_ = json.Unmarshal([]byte(mcpSecretInput), &want)
				if !reflect.DeepEqual(got, want) {
					t.Fatal("redacted input was executed instead of immutable original")
				}
				row, err := f.client.KaguyaApproval.Query().Only(f.ctx)
				state := kaguyaapproval.ExecutionStateSucceeded
				if outcome != "success" {
					state = kaguyaapproval.ExecutionStateFailed
				}
				if err != nil || row.Status != kaguyaapproval.StatusConsumed || row.ExecutionState != state || row.InputHash != permission.HashInput(mcpSecretInput) {
					t.Fatal("MCP execution audit lost error state or binding", err)
				}
				history := f.request(t, http.MethodGet, "/v1/chat/conversation/"+conversation+"/turns", "")
				var page struct {
					Code int `json:"code"`
					Data struct {
						Items []dtochat.StoredTurn `json:"items"`
					} `json:"data"`
				}
				decodeErr := json.NewDecoder(history.Body).Decode(&page)
				history.Body.Close()
				if decodeErr != nil || page.Code != dtocode.SystemSuccess.Code || len(page.Data.Items) != 1 || len(page.Data.Items[0].Approvals) != 1 {
					t.Fatal("history missing MCP authorization audit", decodeErr)
				}
				if page.Data.Items[0].Approvals[0].Input != "" || page.Data.Items[0].Approvals[0].ExecutionState != string(state) {
					t.Fatal("history audit exposed raw parameters or claimed success")
				}
				encoded, _ := json.Marshal(page)
				for _, secret := range []string{mcpSecretProvider, mcpSecretHeader, mcpSecretEnv, mcpSecretRPCError} {
					if strings.Contains(string(encoded), secret) {
						t.Fatal("configuration/protocol secret crossed history boundary")
					}
				}
				if outcome != "protocol_error" && !strings.Contains(string(encoded), mcpSecretResult) {
					t.Fatal("history lost valid user-visible tool body")
				}
				if transport != "stdio" && authenticated.Load() == 0 {
					t.Fatal("real authenticated MCP HTTP path was not exercised")
				}
				// An idempotent decision response is historical state, not a new grant.
				repeated := f.request(t, http.MethodPost, "/v1/chat/conversation/"+conversation+"/approvals/"+pending.ApprovalID, `{"decision":"approve"}`)
				var decision struct {
					Code int                          `json:"code"`
					Data dtochat.ApprovalDecisionResp `json:"data"`
				}
				decodeErr = json.NewDecoder(repeated.Body).Decode(&decision)
				repeated.Body.Close()
				if decodeErr != nil || decision.Code != dtocode.SystemSuccess.Code || decision.Data.Status != "consumed" {
					t.Fatal("repeated MCP decision lost its persisted historical state", decodeErr)
				}
				if data, err := os.ReadFile(filepath.Join(markerDir, "calls.log")); err != nil || string(data) != "called\n" {
					t.Fatal("MCP call replayed or did not execute exactly once", err)
				}
				requireSecretAbsentFromLogs(t, f.logs, "fake-mcp-input-key", "fake-mcp-input-auth", "fake-mcp-input-secret", mcpSecretHeader, mcpSecretEnv, mcpSecretProvider, mcpSecretResult, mcpSecretRPCError)
				manager.Close()
			})
		}
	}
}

func requireMCPDisplayCopy(t *testing.T, input string) {
	t.Helper()
	var value struct {
		APIKey string `json:"api_key"`
		Nested []struct {
			Authorization string `json:"Authorization"`
			Secret        string `json:"client_secret"`
		} `json:"nested"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(input), &value); err != nil || value.APIKey != "[REDACTED]" || len(value.Nested) != 1 || value.Nested[0].Authorization != "[REDACTED]" || value.Nested[0].Secret != "[REDACTED]" || value.Note != "keep-this-note" {
		t.Fatal("approval display did not redact only its secret fields", err)
	}
}

func TestMCPSecretBoundaryHelperProcess(t *testing.T) {
	if os.Getenv("KAGUYA_MCP_SECRET_HELPER") != "1" {
		t.Skip("stdio secret-boundary fixture subprocess only")
	}
	if os.Getenv("KAGUYA_MCP_FAKE_SECRET") != mcpSecretEnv {
		os.Exit(2)
	}
	outcome := os.Getenv("KAGUYA_MCP_SECRET_OUTCOME")
	if outcome != "success" && outcome != "tool_error" && outcome != "protocol_error" {
		os.Exit(2)
	}
	if err := secretBoundaryMCPServer(".", outcome).Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
