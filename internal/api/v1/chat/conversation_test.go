package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

type historyAPIID struct{ value atomic.Int64 }

func (g *historyAPIID) GenID() (int64, error) { return g.value.Add(1), nil }

func TestConversationAPI(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:history-api?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = client, &historyAPIID{}, zap.NewNop()
	defer func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger }()
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := client.KaguyaConversation.Create().SetID("123").SetTitle("Kubernetes 分析").SetModelID("test").SetModelName("test").SetLastMessageAt(at).SetTurnCount(2).Save(ctx); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2; i++ {
		turn, err := client.KaguyaChatTurn.Create().SetConversationID("123").SetTurnIndex(i).SetUserContent(fmt.Sprintf("question %d", i)).
			SetProviderID("p").SetProviderName("test").SetModelID("test").SetModelName("test").SetAPIProtocol("openai-chat").
			SetStartedAt(at).SetFinishedAt(at).SetDurationMs(0).SetToolCalls(0).SetFinishReason("stop").
			SetInputTokens(1).SetOutputTokens(2).SetTotalTokens(3).SetCachedTokens(0).SetReasoningTokens(0).
			SetMessages([]fantasy.Message{fantasy.NewUserMessage("private-context")}).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.KaguyaChatBlock.Create().SetTurnID(turn.ID).SetSequence(1).SetType(kaguyachatblock.TypeText).
			SetText(fmt.Sprintf("answer %d", i)).SetStartedAt(at).SetFinishedAt(at).SetStartOrder(1).SetEndOrder(2).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	api := &ChatApiV1Group{}
	router.GET("/conversation/page", api.ConversationPage)
	router.GET("/conversation/:id", api.ConversationDetail)
	router.GET("/conversation/:id/turns", api.ConversationTurns)
	router.GET("/conversation/:id/title/wait", api.ConversationTitleWait)
	router.POST("/conversation/:id/title/wait", api.ConversationTitleGenerate)
	router.PUT("/conversation/:id", api.ConversationUpdate)
	router.DELETE("/conversation/:id", api.ConversationDelete)
	request := func(method, path, body string, wantCode int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var result struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s: %v", w.Body, err)
		}
		if result.Code != wantCode {
			t.Fatalf("%s %s: %s", method, path, w.Body)
		}
		if strings.Contains(w.Body.String(), "private-context") {
			t.Fatal("exposed private context")
		}
		return result.Data
	}
	ok, bad, missing := dtocode.SystemSuccess.Code, dtocode.RequestParameterError.Code, dtocode.ConversationNotFound.Code
	list := request("GET", "/conversation/page", "", ok)
	if list["total"] != float64(1) || list["page_size"] != float64(20) {
		t.Fatalf("list: %+v", list)
	}
	request("GET", "/conversation/page?page_size=101", "", bad)
	request("GET", "/conversation/123/turns?limit=0", "", bad)
	request("GET", "/conversation/123/turns?before=-1", "", bad)
	page := request("GET", "/conversation/123/turns?limit=1", "", ok)
	if page["has_more"] != true || page["next_before"] != float64(2) {
		t.Fatalf("cursor: %+v", page)
	}
	older := request("GET", "/conversation/123/turns?limit=1&before=2", "", ok)
	if older["has_more"] != false {
		t.Fatalf("older: %+v", older)
	}
	items := older["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["turn_index"] != float64(1) {
		t.Fatalf("older order: %+v", older)
	}
	request("PUT", "/conversation/123", `{}`, bad)
	request("PUT", "/conversation/123", `{"title":"   "}`, bad)
	request("PUT", "/conversation/123", `{"title":"重命名","favorite":true}`, ok)
	detail := request("GET", "/conversation/123", "", ok)
	if detail["title"] != "重命名" || detail["favorite"] != true {
		t.Fatalf("detail: %+v", detail)
	}
	request("PUT", "/conversation/123", `{"favorite":false}`, ok)
	list = request("GET", "/conversation/page?favorite=true", "", ok)
	if list["total"] != float64(0) {
		t.Fatalf("favorite: %+v", list)
	}
	title := request("GET", "/conversation/123/title/wait", "", ok)
	if title["id"] != "123" || title["title"] != "重命名" || len(title) != 2 {
		t.Fatalf("title: %+v", title)
	}
	title = request("POST", "/conversation/123/title/wait", "", ok)
	if title["id"] != "123" || title["title"] != "重命名" || len(title) != 2 {
		t.Fatalf("generation must preserve manual title: %+v", title)
	}
	request("POST", "/conversation/"+strings.Repeat("1", 65)+"/title/wait", "", bad)
	request("POST", "/conversation/unknown/title/wait", "", missing)
	request("GET", "/conversation/"+strings.Repeat("1", 65)+"/title/wait", "", bad)
	request("GET", "/conversation/unknown/title/wait", "", missing)
	request("GET", "/conversation/unknown", "", missing)
	request("PUT", "/conversation/123", `{"title":"新对话"}`, ok)
	request("POST", "/conversation/123/title/wait", "", dtocode.TaskModelNotConfigured.Code)
	request("DELETE", "/conversation/123", "", ok)
	request("POST", "/conversation/123/title/wait", "", missing)
	request("GET", "/conversation/123/title/wait", "", missing)
	request("GET", "/conversation/123/turns", "", missing)
	request("GET", "/conversation/123", "", missing)
	request("PUT", "/conversation/123", `{"title":"resurrect"}`, missing)
	request("DELETE", "/conversation/123", "", missing)
}
