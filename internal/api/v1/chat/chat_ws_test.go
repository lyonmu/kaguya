package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

type wsAPIID struct{ value atomic.Int64 }

func (g *wsAPIID) GenID() (int64, error) { return g.value.Add(1), nil }

// WebSocket 对话必须像 SSE 一样把 project_id 和 files 交给服务层，否则项目工具和
// 文件引用会被静默丢弃。
func TestChatWebSocketForwardsProjectAndFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	t.Setenv("HOME", home)

	client, err := ent.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = client, &wsAPIID{}, zap.NewNop()
	defer func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger }()
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const marker = "WEBSOCKET-FILE-CONTENT-MARKER"
	if err := os.WriteFile(filepath.Join(projectDir, "notes.txt"), []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := client.KaguyaProject.Create().SetName("项目").SetPath(projectDir).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var prompt atomic.Value
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt.Store(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer providerServer.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("ws").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(providerServer.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := &ChatApiV1Group{}
	engine.GET("/v1/chat/ws", api.ChatWS)
	server := httptest.NewServer(engine)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/chat/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(dtochat.ChatReq{Flag: dtochat.WSFlagChat, Messages: "总结这个文件", ProjectID: project.ID, Files: []string{"notes.txt"}}); err != nil {
		t.Fatal(err)
	}

	flags := []dtochat.WSFlag{}
	for {
		var frame struct {
			Code int              `json:"code"`
			Data dtochat.ChatResp `json:"data"`
		}
		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Code != 100000 || frame.Data.Err != nil {
			t.Fatalf("unexpected frame: %+v", frame)
		}
		flags = append(flags, frame.Data.Chat.Flag)
		if frame.Data.Chat.Flag == dtochat.WSFlagDone {
			break
		}
	}
	if len(flags) < 2 || flags[0] != dtochat.WSFlagStart || flags[len(flags)-1] != dtochat.WSFlagDone {
		t.Fatalf("unexpected lifecycle: %v", flags)
	}
	body := prompt.Load()
	if body == nil {
		t.Fatal("provider did not receive a request")
	}
	if !strings.Contains(body.(string), marker) {
		t.Fatalf("referenced file content missing from upstream prompt: %s", body)
	}
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body.(string)), &request); err != nil || len(request.Messages) == 0 {
		t.Fatalf("invalid upstream request: %v", err)
	}
	referenced := false
	for _, message := range request.Messages {
		if message.Role != "system" && strings.Contains(string(message.Content), marker) {
			referenced = true
		}
	}
	if !referenced {
		t.Fatalf("referenced file content missing from user messages: %s", body)
	}
}
