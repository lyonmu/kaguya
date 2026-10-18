package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

// setupApprovalAPI 建立审批接口所需的数据库与路由。
func setupApprovalAPI(t *testing.T) (*ent.Client, *gin.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	client, err := ent.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = client, &sseAPIID{}, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := &ChatApiV1Group{}
	engine.POST("/v1/chat/sse", api.ChatSSE)
	engine.GET("/v1/chat/conversation/:id/approvals", api.ConversationApprovals)
	engine.POST("/v1/chat/conversation/:id/approvals/:approval_id", api.ApprovalDecision)
	return client, engine
}

func seedApprovalConversation(t *testing.T, client *ent.Client, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := client.KaguyaConversation.Create().SetID(id).SetTitle("审批测试").
		SetInstructionsFormat("split").SetModelID("m").SetModelName("m").SetLastMessageAt(time.Now()).Save(ctx); err != nil {
		t.Fatal(err)
	}
}

// 未知/他轮回的审批不能裁决，统一返回 102012 而不是泄露存在性。
func TestApprovalDecisionNotFoundAndConflict(t *testing.T) {
	client, engine := setupApprovalAPI(t)
	seedApprovalConversation(t, client, "c1")
	body := strings.NewReader(`{"decision":"approve"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/conversation/c1/approvals/missing", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 102012 {
		t.Fatalf("missing approval code = %d, want 102012", payload.Code)
	}

	// 已终结的审批再次裁决属于冲突。
	if _, err := client.KaguyaApproval.Create().SetApprovalID("done").SetConversationID("c1").SetTurnID("t1").
		SetToolCallID("call-1").SetInputHash("hash").SetToolIdentity("builtin:write").
		SetStatus(kaguyaapproval.StatusRejected).SetDecision("reject").SetRequestedAt(time.Now()).Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/chat/conversation/c1/approvals/done", strings.NewReader(`{"decision":"approve"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	payload.Code = 0
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 102013 {
		t.Fatalf("conflicting decision code = %d, want 102013", payload.Code)
	}

	// 非法 decision 由 binding 拒绝。
	request = httptest.NewRequest(http.MethodPost, "/v1/chat/conversation/c1/approvals/done", strings.NewReader(`{"decision":"maybe"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	payload.Code = 0
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 100001 {
		t.Fatalf("invalid decision code = %d, want 100001", payload.Code)
	}
}

// 孤立的持久 pending 行不是当前可执行入口，GET 不能复活崩溃遗留审批。
func TestConversationApprovalsDoesNotRestoreOrphanedPending(t *testing.T) {
	client, engine := setupApprovalAPI(t)
	seedApprovalConversation(t, client, "c1")
	ctx := context.Background()
	for _, item := range []struct {
		id     string
		status kaguyaapproval.Status
	}{{"p1", kaguyaapproval.StatusPending}, {"p2", kaguyaapproval.StatusCanceled}} {
		if _, err := client.KaguyaApproval.Create().SetApprovalID(item.id).SetConversationID("c1").SetTurnID("t1").
			SetToolCallID(item.id).SetInputHash("hash").SetToolIdentity("builtin:write").
			SetStatus(item.status).SetRequestedAt(time.Now()).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/chat/conversation/c1/approvals", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var payload struct {
		Code int `json:"code"`
		Data []struct {
			ApprovalID string `json:"approval_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 100000 || len(payload.Data) != 0 {
		t.Fatalf("approvals payload = %+v", payload)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/chat/conversation/missing/approvals", nil)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	payload.Code = 0
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 102003 {
		t.Fatalf("missing conversation code = %d, want 102003", payload.Code)
	}
}

// 非法 permission_mode 在进入服务层前由 binding 拒绝；续聊覆写由服务层拒绝。
func TestChatSSEValidatesPermissionMode(t *testing.T) {
	_, engine := setupApprovalAPI(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/sse", strings.NewReader(`{"messages":"hi","permission_mode":"future_mode"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response %q: %v", response.Body.String(), err)
	}
	if payload.Code != 100001 {
		t.Fatalf("invalid permission mode code = %d, want 100001", payload.Code)
	}
}
