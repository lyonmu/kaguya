package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// fakeDocumentReader 复用项目路径读取接口的测试替身。
type fakeDocumentReader struct {
	docs map[string]*Document
	err  error
}

func (f *fakeDocumentReader) ReadDocument(_ context.Context, projectID, path string, maxBytes int64) (*Document, error) {
	if f.err != nil {
		return nil, f.err
	}
	doc := f.docs[projectID+"\x00"+path]
	if doc == nil {
		return nil, errors.New("document not found")
	}
	if int64(len(doc.Content)) > maxBytes {
		return nil, errors.New("document too large")
	}
	return doc, nil
}

// 资料导入完整链路：Import → 快照来源 → Worker 编译 → FTS 检索。
func TestImportDocumentCompilesAndRetrieves(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	scope := ScopeKey(projectID)
	content := "项目规范：继续使用 SQLCipher，不引入第二个数据库。"
	reader := &fakeDocumentReader{docs: map[string]*Document{
		projectID + "\x00docs/spec.md": {Path: "docs/spec.md", Content: content, Size: int64(len(content))},
	}}
	svc.WithDocumentReader(reader)

	imported, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Deduplicated || imported.SourceID == "" || imported.State != string(kaguyamemorysource.StatePending) {
		t.Fatalf("import=%+v", imported)
	}
	src, err := client.KaguyaMemorySource.Get(ctx, imported.SourceID)
	if err != nil || src.Kind != kaguyamemorysource.KindImport || src.DocumentPath != "docs/spec.md" ||
		src.RawContent != content || src.ContentHash == "" {
		t.Fatalf("source=%+v err=%v", src, err)
	}

	caller := &fakeCaller{}
	svc = memoryServiceWith(svc, caller)
	worker := NewWorker(svc)
	backdateSources(t, ctx, client, 2*time.Minute)
	job, _ := worker.claimReadyBatch(ctx, "")
	if job == nil {
		t.Fatal("missing compile job")
	}
	candidate := dtomemory.Candidate{
		Key: "sqlcipher-constraint", Kind: "decision",
		Title: "SQLCipher 单后端", Statement: "项目继续使用 SQLCipher。",
		Aliases: []string{"数据库"}, Basis: "document_statement",
		Evidence: []dtomemory.CandidateEvidence{{
			SourceID: src.ID, PartKey: "document", Quote: "继续使用 SQLCipher",
		}},
	}
	change := dtomemory.PagePatch{
		Action: "create", CandidateKeys: []string{"sqlcipher-constraint"},
		CanonicalKey: "sqlcipher-constraint", Kind: "decision",
		Title: "SQLCipher 单后端", Summary: "资料导入",
		Body:    pageBody("导入资料结论"),
		Aliases: []string{"数据库"},
		Claims: []dtomemory.ClaimPatch{{
			Key: "storage", Statement: "项目继续使用 SQLCipher", Basis: "document_statement",
			Evidence: []dtomemory.ClaimEvidence{{
				SourceID: src.ID, PartKey: "document", Quote: "继续使用 SQLCipher", Relation: "support",
			}},
		}},
		RelatedIDs: []string{}, Reason: "资料导入",
	}
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(candidate)), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
	}
	svc.runJob(ctx, job)

	page, err := client.KaguyaMemoryPage.Query().Only(ctx)
	if err != nil || page.Status != "active" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	hits, err := svc.SearchPages(ctx, []string{scope}, "SQLCipher", 5, false)
	if err != nil || len(hits) != 1 || hits[0].ID != page.ID {
		t.Fatalf("retrieval hits=%+v err=%v", hits, err)
	}
	src, err = client.KaguyaMemorySource.Get(ctx, imported.SourceID)
	if err != nil || src.State != kaguyamemorysource.StateProcessed {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}

// 重复导入幂等：同路径同内容返回已有来源；内容变化产生新来源且历史证据保留。
func TestImportDocumentIdempotent(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	scope := ScopeKey(projectID)
	reader := &fakeDocumentReader{docs: map[string]*Document{
		projectID + "\x00docs/spec.md": {Path: "docs/spec.md", Content: "第一版内容", Size: 12},
	}}
	svc.WithDocumentReader(reader)

	first, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"})
	if err != nil || second.SourceID != first.SourceID || !second.Deduplicated {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
	reader.docs[projectID+"\x00docs/spec.md"].Content = "第二版内容"
	third, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"})
	if err != nil || third.SourceID == first.SourceID || third.Deduplicated {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 2 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
}

// 导入输入校验：必须项目范围、读取器已注入、记忆已启用且资料有可见文本。
func TestImportDocumentRejectsInvalidInput(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	scope := ScopeKey(projectID)

	// 未注入读取器。
	if _, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"}); err == nil {
		t.Fatal("missing reader must be rejected")
	}
	reader := &fakeDocumentReader{docs: map[string]*Document{}}
	svc.WithDocumentReader(reader)
	// 个人范围不允许导入资料。
	if _, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: ScopePersonal, Path: "docs/spec.md"}); err == nil {
		t.Fatal("personal scope import must be rejected")
	}
	// 缺失项目。
	if _, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: "project:missing", Path: "docs/spec.md"}); err == nil {
		t.Fatal("missing project must be rejected")
	}
	// 空内容。
	reader.docs[projectID+"\x00docs/empty.md"] = &Document{Path: "docs/empty.md", Content: "  \n  "}
	if _, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/empty.md"}); err == nil {
		t.Fatal("empty document must be rejected")
	}
	// 关闭记忆总开关。
	setupPolicy(t, ctx, client, false, false)
	reader.docs[projectID+"\x00docs/spec.md"] = &Document{Path: "docs/spec.md", Content: "内容", Size: 6}
	if _, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/spec.md"}); err == nil {
		t.Fatal("disabled memory import must be rejected")
	}
}

// 导入来源导航返回资料路径、有界片段并脱敏。
func TestImportSourceNavigation(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	scope := ScopeKey(projectID)
	reader := &fakeDocumentReader{docs: map[string]*Document{
		projectID + "\x00docs/secret.md": {Path: "docs/secret.md", Content: "配置 api_key=sk-abcdefgh12345678\n正常内容", Size: 60},
	}}
	svc.WithDocumentReader(reader)
	imported, err := svc.ImportDocument(ctx, &dtomemory.MemoryImportReq{ScopeKey: scope, Path: "docs/secret.md"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.SourceDetail(ctx, imported.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Available || detail.Kind != "import" || detail.DocumentPath != "docs/secret.md" {
		t.Fatalf("detail=%+v", detail)
	}
	if len(detail.Parts) != 1 || strings.Contains(detail.Parts[0].Text, "sk-abcdefgh12345678") {
		t.Fatalf("parts=%+v", detail.Parts)
	}
	if !strings.Contains(detail.Parts[0].Text, "正常内容") {
		t.Fatalf("parts=%+v", detail.Parts)
	}
}

// 未导入任何资料时列表为空；状态过滤返回对应来源。
func TestListSourcesFilters(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-list", projectID, kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "列表来源")
	captureTurn(t, ctx, client, conv, turnID)

	all, err := svc.ListSources(ctx, &dtomemory.MemorySourceListReq{Page: 1, PageSize: 10})
	if err != nil || all.Total != 1 || len(all.Items) != 1 || all.Items[0].Kind != "turn" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	turns, err := svc.ListSources(ctx, &dtomemory.MemorySourceListReq{Kind: "turn", State: "pending", Page: 1, PageSize: 10})
	if err != nil || turns.Total != 1 {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
	none, err := svc.ListSources(ctx, &dtomemory.MemorySourceListReq{Kind: "import", Page: 1, PageSize: 10})
	if err != nil || none.Total != 0 {
		t.Fatalf("imports=%+v err=%v", none, err)
	}
}
