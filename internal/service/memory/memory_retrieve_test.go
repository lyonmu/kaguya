package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
)

// 中文召回：两字词、连续中文、混合 SQLCipher、路径、版本号与标点都能命中。
func TestChineseSearchRecall(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	pages := map[string]string{
		"加密": "数据库加密策略：SQLCipher 静态加密覆盖 WAL 与备份。",
		"打包": "make package-macos 组装 Kaguya.app；MACOSX_DEPLOYMENT_TARGET=14.0。",
		"依赖": "go-sqlite3 使用 USE_LIBSQLITE3 链接 target/sqlcipher/lib/libsqlite3.a。",
		"版本": "SQLCipher 4.19.0 community 与 SQLite 3.53.4 已验证。",
	}
	for title, body := range pages {
		if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
			ScopeKey: ScopePersonal, Kind: "fact", Title: title + "记录", Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query, want string
	}{
		{"加密", "数据库加密策略"},
		{"标题", ""}, // 无匹配时不为凑满塞无关页面
		{"SQLCipher", "数据库加密策略"},
		{"package-macos", "make package-macos"},
		{"go-sqlite3", "USE_LIBSQLITE3"},
		{"MACOSX_DEPLOYMENT_TARGET", "MACOSX_DEPLOYMENT_TARGET"},
		{"libsqlite3.a", "libsqlite3.a"},
		{"4.19.0", "SQLite 3.53.4"},
		{"打包失败怎么办", "make package-macos"},
		{"加密、备份", "数据库加密策略"},
	} {
		hits, err := svc.SearchPages(ctx, []string{ScopePersonal}, tc.query, 5, true)
		if err != nil {
			t.Fatalf("query %q: %v", tc.query, err)
		}
		found := false
		for _, hit := range hits {
			if strings.Contains(hit.Summary, tc.want) || strings.Contains(hit.Body, tc.want) {
				found = true
			}
		}
		if tc.want == "" && len(hits) != 0 {
			t.Fatalf("query %q must return nothing, got %+v", tc.query, hits)
		}
		if tc.want != "" && !found {
			t.Fatalf("query %q missed %q: %+v", tc.query, tc.want, hits)
		}
	}
}

// 查询安全：双引号、OR/NOT/NEAR、超长查询与大量 term 都被转义与限额。
func TestSearchQuerySafety(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "安全", Body: "内容 OR NEAR(a b)",
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`" OR kaguya_memory_fts MATCH '`,
		`注入" OR NOT NEAR(x y) OR "`,
		strings.Repeat("很长的查询", 500),
		strings.Repeat(`"词" `, 200),
		"   ",
		"\"\"\"\"",
	} {
		if _, err := svc.SearchPages(ctx, []string{ScopePersonal}, query, 5, true); err != nil {
			t.Fatalf("unsafe query %q must not error: %v", query, err)
		}
	}
}

// 作用域硬隔离：p-1 不读 p-2；普通对话不读项目；项目对话不默认读个人。
func TestSearchScopeIsolation(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	p1 := makeProject(t, ctx, mustClient(ctx, svc), "p1")
	p2 := makeProject(t, ctx, mustClient(ctx, svc), "p2")
	mustPage := func(scope, body string) {
		t.Helper()
		if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
			ScopeKey: scope, Kind: "fact", Title: "约束", Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustPage(ScopePersonal, "个人私事：孩子学校安排")
	mustPage(ScopeShared, "通用偏好：回答用中文")
	mustPage("project:"+p1, "项目一：使用 SQLCipher")
	mustPage("project:"+p2, "项目二：使用 Postgres")

	assertScope := func(scopes []string, want, refuse string) {
		t.Helper()
		hits, err := svc.SearchPages(ctx, scopes, "约束", 10, true)
		if err != nil {
			t.Fatal(err)
		}
		text := ""
		for _, hit := range hits {
			text += hit.Body
		}
		if !strings.Contains(text, want) {
			t.Fatalf("scopes=%v missing %q in %+v", scopes, want, hits)
		}
		if strings.Contains(text, refuse) {
			t.Fatalf("scopes=%v leaked %q in %+v", scopes, refuse, hits)
		}
	}
	assertScope(RecallScopes(""), "回答用中文", "项目一")
	assertScope(RecallScopes(p1), "项目一", "Postgres")
	assertScope(RecallScopes(p1), "回答用中文", "孩子学校")
	assertScope(RecallScopes(p2), "Postgres", "SQLCipher")

	// 工具读取同样做范围校验。
	reader := NewScopedReader(svc, RecallScopes(p1))
	if _, err := reader.ReadMemory(ctx, mustFindPage(t, ctx, svc, "Postgres"), 0); err != ErrPageForbidden {
		t.Fatalf("cross-project read: %v", err)
	}
}

// mustClient 返回服务持有的 client（测试辅助）。
func mustClient(_ context.Context, svc *Service) *ent.Client { return svc.client }

func mustFindPage(t *testing.T, ctx context.Context, svc *Service, marker string) string {
	t.Helper()
	rows, err := svc.client.KaguyaMemoryPage.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Body, marker) {
			return row.ID
		}
	}
	t.Fatalf("page with %q not found", marker)
	return ""
}

// 过期页面默认不自动注入；显式搜索可返回并标记。
func TestExpiredPageHandling(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	past := time.Now().Add(-time.Hour)
	if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "procedure", Title: "过期方法",
		Body: "临时端口转发方法", ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := svc.SearchPages(ctx, []string{ScopePersonal}, "端口", 5, false)
	if err != nil || len(hits) != 0 {
		t.Fatalf("auto recall must skip expired: %+v err=%v", hits, err)
	}
	hits, err = svc.SearchPages(ctx, []string{ScopePersonal}, "端口", 5, true)
	if err != nil || len(hits) != 1 || !hits[0].Expired {
		t.Fatalf("explicit search must flag expired: %+v err=%v", hits, err)
	}
	selection, err := svc.AutoRecall(ctx, RecallOptions{Scopes: []string{ScopePersonal}, Query: "端口", ContextTokens: 2000, Window: 128000})
	if err != nil || selection.Text != "" {
		t.Fatalf("auto recall injected expired page: %+v err=%v", selection, err)
	}
}

// 预算内召回：注入有字节与 token 上限，冻结选择记录页面版本。
func TestAutoRecallBudgetAndSelection(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	for i := 0; i < 3; i++ {
		if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
			ScopeKey: ScopePersonal, Kind: "fact", Title: "加密方案" + string(rune('A'+i)),
			Summary: "摘要内容", Body: strings.Repeat("加密相关内容。", 200),
		}); err != nil {
			t.Fatal(err)
		}
	}
	selection, err := svc.AutoRecall(ctx, RecallOptions{
		Scopes: []string{ScopePersonal}, Query: "加密", ContextTokens: 2000, Window: 128000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Refs) == 0 || selection.EstimatedTokens == 0 || selection.RetrieverVersion != RetrieverVersion {
		t.Fatalf("selection=%+v", selection)
	}
	if !strings.Contains(selection.Text, "不是当前用户请求") || !strings.Contains(selection.Text, "[memory:") {
		t.Fatalf("context text: %q", selection.Text)
	}
	// 预算极小时不注入，不为凑满塞无关页面。
	tiny, err := svc.AutoRecall(ctx, RecallOptions{
		Scopes: []string{ScopePersonal}, Query: "加密", ContextTokens: 10, Window: 128000,
	})
	if err != nil || tiny.Text != "" {
		t.Fatalf("tiny budget: %+v err=%v", tiny, err)
	}
}

// 运行中删除/禁用后临时 selection 立即失效，不继续附加已撤销页面。
func TestRenderTransientRevokesRevokedPages(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	setupPolicy(t, ctx, mustClient(ctx, svc), true, true)
	detail, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "待撤销", Body: "将被删除的内容",
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := []dtomemory.TurnMemoryRef{{PageID: detail.ID, Version: detail.Version}}
	text, kept, err := svc.RenderTransient(ctx, refs)
	if err != nil || len(kept) != 1 || !strings.Contains(text, "待撤销") {
		t.Fatalf("text=%q kept=%+v err=%v", text, kept, err)
	}
	// 停用立即失效。
	if err := svc.DeletePage(ctx, detail.ID, "disable"); err != nil {
		t.Fatal(err)
	}
	text, kept, err = svc.RenderTransient(ctx, refs)
	if err != nil || len(kept) != 0 || text != "" {
		t.Fatalf("revoked page still injected: text=%q kept=%+v err=%v", text, kept, err)
	}
	// 遗忘后历史版本不可绕过删除。
	if err := svc.DeletePage(ctx, detail.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	text, kept, err = svc.RenderTransient(ctx, refs)
	if err != nil || len(kept) != 0 || text != "" {
		t.Fatalf("forgotten page injected: text=%q kept=%+v err=%v", text, kept, err)
	}
	// 引用展示显示“已删除”。
	resp, err := svc.TurnMemoryRefs(ctx, dtomemory.TurnMemorySelection{Refs: refs})
	if err != nil || len(resp.Items) != 1 || !resp.Items[0].Deleted || resp.Items[0].Title != "已删除" {
		t.Fatalf("refs=%+v err=%v", resp, err)
	}
}
