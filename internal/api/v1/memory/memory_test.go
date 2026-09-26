package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	svcmemory "github.com/lyonmu/kaguya/internal/service/memory"
	"go.uber.org/zap"
)

type memoryAPIID struct{ value atomic.Int64 }

func (g *memoryAPIID) GenID() (int64, error) { return g.value.Add(1), nil }

func setupMemoryAPITest(t *testing.T) *gin.Engine {
	t.Helper()
	conn, err := ent.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	if err := conn.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = conn, &memoryAPIID{}, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	if err := initialize.Run(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureMemoryFTS(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := svcmemory.EnsureSearchProjection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetMemoryEnabled(true).SetMemoryAutoCapture(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	api, router := &MemoryApiV1Group{}, gin.New()
	router.GET("/pages", api.MemoryPageList)
	router.GET("/pages/:id", api.MemoryPageDetail)
	router.POST("/pages", api.MemoryPageCreate)
	router.PATCH("/pages/:id", api.MemoryPageUpdate)
	router.DELETE("/pages/:id", api.MemoryPageDelete)
	router.GET("/pages/:id/revisions", api.MemoryPageRevisions)
	router.GET("/pages/:id/diff", api.MemoryPageDiff)
	router.POST("/pages/:id/restore", api.MemoryPageRestore)
	router.GET("/sources", api.MemorySourceList)
	router.GET("/sources/:id", api.MemorySourceDetail)
	router.POST("/backfill", api.MemoryBackfill)
	router.POST("/import", api.MemoryImport)
	router.GET("/jobs", api.MemoryJobList)
	router.POST("/jobs/:id/retry", api.MemoryJobRetry)
	router.POST("/jobs/:id/approve", api.MemoryJobApprove)
	router.POST("/jobs/:id/reject", api.MemoryJobReject)
	router.POST("/compile", api.MemoryCompile)
	router.GET("/status", api.MemoryStatus)
	router.POST("/export", api.MemoryExport)
	return router
}

func memoryAPIRequest(t *testing.T, router *gin.Engine, method, path, body string, wantCode int) map[string]any {
	t.Helper()
	writer := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(writer, req)
	var resp struct {
		Code int `json:"code"`
		Data any `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", writer.Body.String(), err)
	}
	if resp.Code != wantCode {
		t.Fatalf("%s %s code=%d want=%d body=%s", method, path, resp.Code, wantCode, writer.Body.String())
	}
	if data, ok := resp.Data.(map[string]any); ok {
		return data
	}
	return nil
}

// 记忆管理 API 闭环：人工保存、编辑冲突、修订、恢复、停用、导出与状态。
func TestMemoryAPIPageLifecycle(t *testing.T) {
	router := setupMemoryAPITest(t)
	data := memoryAPIRequest(t, router, "POST", "/pages", `{
		"scope_key":"personal","kind":"decision","title":"Memory 使用 SQLCipher",
		"summary":"不新增第二个数据库","body":"## 决定\n继续使用 SQLCipher。",
		"aliases":["记忆存储"]
	}`, dtocode.SystemSuccess.Code)
	id, _ := data["id"].(string)
	if id == "" || data["status"] != "active" || data["version"] != float64(1) {
		t.Fatalf("create: %+v", data)
	}

	detail := memoryAPIRequest(t, router, "GET", "/pages/"+id, "", dtocode.SystemSuccess.Code)
	if detail["body"] != "## 决定\n继续使用 SQLCipher。" {
		t.Fatalf("detail: %+v", detail)
	}

	// 编辑冲突返回明确错误，而非最后写入者悄悄覆盖。
	memoryAPIRequest(t, router, "PATCH", "/pages/"+id,
		`{"expected_version":1,"summary":"更新后摘要"}`, dtocode.SystemSuccess.Code)
	memoryAPIRequest(t, router, "PATCH", "/pages/"+id,
		`{"expected_version":1,"summary":"再次更新"}`, dtocode.MemoryVersionConflict.Code)

	list := memoryAPIRequest(t, router, "GET", "/pages?scope_key=personal&keyword=SQLCipher", "", dtocode.SystemSuccess.Code)
	if list["total"] != float64(1) {
		t.Fatalf("list: %+v", list)
	}

	revisions := memoryAPIRequest(t, router, "GET", "/pages/"+id+"/revisions", "", dtocode.SystemSuccess.Code)
	_ = revisions

	// 停用移出召回，恢复旧修订产生新版本。
	memoryAPIRequest(t, router, "DELETE", "/pages/"+id+"?mode=disable", "", dtocode.SystemSuccess.Code)
	restored := memoryAPIRequest(t, router, "POST", "/pages/"+id+"/restore", `{"version":1}`, dtocode.SystemSuccess.Code)
	if restored["version"] != float64(4) || restored["status"] != "active" {
		t.Fatalf("restore: %+v", restored)
	}

	export := memoryAPIRequest(t, router, "POST", "/export", `{"scope_keys":["personal"]}`, dtocode.SystemSuccess.Code)
	markdown, _ := export["markdown"].(string)
	if !strings.Contains(markdown, "Memory 使用 SQLCipher") || !strings.Contains(markdown, "导出时间") {
		t.Fatalf("export: %q", markdown)
	}

	status := memoryAPIRequest(t, router, "GET", "/status", "", dtocode.SystemSuccess.Code)
	if status["enabled"] != true || status["index_normalizer"] != float64(svcmemory.NormalizerVersion) {
		t.Fatalf("status: %+v", status)
	}

	// 遗忘后不可再读取，也不能通过历史版本绕过。
	memoryAPIRequest(t, router, "DELETE", "/pages/"+id+"?mode=forget", "", dtocode.SystemSuccess.Code)
	memoryAPIRequest(t, router, "GET", "/pages/"+id, "", dtocode.MemoryPageNotFound.Code)
	memoryAPIRequest(t, router, "GET", "/pages/"+id+"?version=1", "", dtocode.MemoryPageNotFound.Code)
}

// 新增接口：版本对比、来源导航、历史回填与导入边界。
func TestMemoryAPIDiffSourcesBackfill(t *testing.T) {
	router := setupMemoryAPITest(t)
	created := memoryAPIRequest(t, router, "POST", "/pages", `{
		"scope_key":"personal","kind":"decision","title":"对比页面",
		"summary":"第一版","body":"第一版正文","aliases":["对比"]
	}`, dtocode.SystemSuccess.Code)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create: %+v", created)
	}
	memoryAPIRequest(t, router, "PATCH", "/pages/"+id,
		`{"expected_version":1,"summary":"第二版","body":"第二版正文"}`, dtocode.SystemSuccess.Code)

	diff := memoryAPIRequest(t, router, "GET", "/pages/"+id+"/diff?from=1&to=2", "", dtocode.SystemSuccess.Code)
	changes, _ := diff["content_changes"].([]any)
	if len(changes) == 0 {
		t.Fatalf("diff: %+v", diff)
	}
	previous := memoryAPIRequest(t, router, "GET", "/pages/"+id+"/diff?from=0&to=2", "", dtocode.SystemSuccess.Code)
	if from, _ := previous["from"].(map[string]any); from["version"] != float64(1) {
		t.Fatalf("previous: %+v", previous)
	}
	memoryAPIRequest(t, router, "GET", "/pages/ghost/diff?from=1", "", dtocode.MemoryPageNotFound.Code)

	sources := memoryAPIRequest(t, router, "GET", "/sources?kind=note", "", dtocode.SystemSuccess.Code)
	if sources["total"] != float64(1) {
		t.Fatalf("sources: %+v", sources)
	}
	items, _ := sources["items"].([]any)
	first, _ := items[0].(map[string]any)
	sourceID, _ := first["id"].(string)
	detail := memoryAPIRequest(t, router, "GET", "/sources/"+sourceID, "", dtocode.SystemSuccess.Code)
	if detail["available"] != true || detail["kind"] != "note" {
		t.Fatalf("source detail: %+v", detail)
	}
	memoryAPIRequest(t, router, "GET", "/sources/ghost", "", dtocode.MemorySourceNotFound.Code)

	backfill := memoryAPIRequest(t, router, "POST", "/backfill",
		`{"scope_key":"personal","max_sources":5}`, dtocode.SystemSuccess.Code)
	if backfill["job_id"] == "" || backfill["status"] != "pending" {
		t.Fatalf("backfill: %+v", backfill)
	}
	// 同一范围进行中作业幂等返回。
	again := memoryAPIRequest(t, router, "POST", "/backfill",
		`{"scope_key":"personal","max_sources":5}`, dtocode.SystemSuccess.Code)
	if again["job_id"] != backfill["job_id"] {
		t.Fatalf("backfill not idempotent: %+v vs %+v", backfill, again)
	}
	memoryAPIRequest(t, router, "POST", "/backfill",
		`{"scope_key":"shared","max_sources":5}`, dtocode.RequestParameterError.Code)
	// 导入必须项目范围且注入读取器；个人范围明确拒绝。
	memoryAPIRequest(t, router, "POST", "/import",
		`{"scope_key":"personal","path":"docs/spec.md"}`, dtocode.RequestParameterError.Code)
}

// 任务接口：空列表、立即整理入队、未知任务的错误码。
func TestMemoryAPIJobs(t *testing.T) {
	router := setupMemoryAPITest(t)
	memoryAPIRequest(t, router, "GET", "/jobs", "", dtocode.SystemSuccess.Code)
	memoryAPIRequest(t, router, "POST", "/compile", `{"scope_key":"personal"}`, dtocode.SystemSuccess.Code)
	memoryAPIRequest(t, router, "POST", "/compile", `{"scope_key":"project:missing"}`, dtocode.RequestParameterError.Code)
	memoryAPIRequest(t, router, "POST", "/jobs/ghost/retry", "", dtocode.MemoryJobNotFound.Code)
	memoryAPIRequest(t, router, "POST", "/jobs/ghost/approve", "", dtocode.MemoryJobNotFound.Code)
	memoryAPIRequest(t, router, "POST", "/pages", `{"scope_key":"bad scope!","kind":"fact","title":"t","body":"b"}`,
		dtocode.RequestParameterError.Code)
}
