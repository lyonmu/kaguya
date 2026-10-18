package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestReferenceIdentityCacheReadsBodyOnceAndKeepsSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "file.txt"), []byte("identity-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "sub", "file.txt"), filepath.Join(dir, "sub", "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/file.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	matcher, err := workspace.NewIgnoreMatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	reader := &referenceReader{filter: workspace.NewReferenceFilter(ctx, root, matcher), ctx: ctx, scanCtx: ctx, budget: 64 << 10, metadata: workspace.ReferenceMetadataBudget{Limit: referenceMetadataBytes}}
	reads := 0
	reader.readWindow = func(ctx context.Context, file *os.File) (workspace.TextWindow, error) {
		reads++
		return workspace.ReadTextWindowFile(ctx, file, 0, referenceLineLimit, referenceMaxFileBytes, referenceLineByteLimit, referenceLineByteLimit)
	}
	for _, ref := range []dtochat.Reference{{Type: "file", Path: "alias.txt"}, {Type: "file", Path: "sub/hard.txt"}, {Type: "directory", Path: "sub"}} {
		if err := reader.addSource(ref); err != nil {
			t.Fatal(err)
		}
	}
	resolution, err := reader.format()
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || strings.Count(resolution.prompt, "identity-marker") != 1 {
		t.Fatalf("reads=%d prompt=%s", reads, resolution.prompt)
	}
	for _, source := range resolution.summary.Sources {
		if !source.Included || source.Hash == "" || len(source.Files) == 0 {
			t.Fatalf("source=%+v", source)
		}
		for _, file := range source.Files {
			if !file.Included || len(file.Hash) != 64 || file.ResolvedPath == "" || len(file.Ranges) != 1 || file.Ranges[0].Start != 1 || file.Ranges[0].End != 1 {
				t.Fatalf("file=%+v", file)
			}
		}
	}
}

func TestReferenceSerializationNeverReturnsOverBudget(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("small-%03d.txt", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	resolution, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{{Type: "directory", Path: "."}}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.prompt) > 1024 {
		t.Fatalf("inline bytes=%d", len(resolution.prompt))
	}
	if !resolution.summary.ScanIncomplete || resolution.summary.PartialReason != "context_budget" || len(resolution.summary.Continuation) != 1 {
		t.Fatalf("missing partial metadata: %+v", resolution.summary)
	}
	if !resolution.summary.Sources[0].Included || len(resolution.summary.Sources[0].Files) == 0 {
		t.Fatalf("no actual content: %+v", resolution.summary)
	}
	if resolution.summary.InlineBytes != int64(len(resolution.prompt)) || resolution.summary.EstimatedTokens != resolution.summary.InlineBytes {
		t.Fatalf("byte accounting: %+v", resolution.summary)
	}
	c := resolution.summary.Continuation[0]
	if c.Root != "." || c.LastPath == "" || c.KnownOmittedFiles != 1 || len(c.UnscannedSubtrees) != 1 || c.UnscannedSubtrees[0] != "." {
		t.Fatalf("continuation: %+v", c)
	}
}

// seedReferenceProject 创建项目记录与真实目录，返回同一测试数据库的上下文。
func seedReferenceProject(t *testing.T) (context.Context, string, string) {
	t.Helper()
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("reference evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := client.KaguyaProject.Create().SetName("refs").SetPath(dir).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, p.ID, dir
}

func TestResolveReferenceExplicitFile(t *testing.T) {
	ctx, projectID, _ := seedReferenceProject(t)
	resolution, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{{Type: "file", Path: "note.txt"}}, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolution.prompt, "reference evidence") {
		t.Fatalf("prompt missing content: %s", resolution.prompt)
	}
	if len(resolution.summary.Sources) != 1 || resolution.summary.Sources[0].Type != "file" {
		t.Fatalf("summary = %+v", resolution.summary)
	}
}

func TestExplicitReferencesCannotBypassFilter(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	for name, body := range map[string]string{
		".gitignore": "private.txt\nignored/\n", "private.txt": "private-marker",
		"ignored/secret.txt": "private-marker", ".git/config": "private-marker", "dist/output.txt": "generated-marker",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("private.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []dtochat.Reference{
		{Type: "file", Path: "private.txt"}, {Type: "file", Path: "ignored/secret.txt"},
		{Type: "file", Path: "alias.txt"}, {Type: "file", Path: ".git/config"},
		{Type: "directory", Path: "ignored"}, {Type: "directory", Path: "dist"},
	} {
		if _, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{ref}, 64<<10); !errors.Is(err, ErrReferenceInvalid) {
			t.Errorf("%+v: %v", ref, err)
		}
	}
}

func TestResolveReferenceDirectoryManifestAndIgnore(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.go"), []byte("package sub"), 0600); err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{{Type: "directory", Path: "."}}, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resolution.prompt, "secret") {
		t.Fatalf("ignored file leaked: %s", resolution.prompt)
	}
	if !strings.Contains(resolution.prompt, "sub/nested.go") || !strings.Contains(resolution.prompt, "package sub") {
		t.Fatalf("nested file missing: %s", resolution.prompt)
	}
	if !strings.Contains(resolution.prompt, "Manifest:") {
		t.Fatalf("manifest missing: %s", resolution.prompt)
	}
}

func TestResolveReferenceRejectsEscapeAndTypeMismatch(t *testing.T) {
	ctx, projectID, _ := seedReferenceProject(t)
	for _, ref := range []dtochat.Reference{
		{Type: "file", Path: "../outside.txt"},
		{Type: "file", Path: "/etc/passwd"},
		{Type: "directory", Path: "note.txt"},
	} {
		if _, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{ref}, 64*1024); err == nil {
			t.Fatalf("reference %+v must be rejected", ref)
		}
	}
}

func TestResolveReferenceBudgetExceededForLargeContent(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	large := strings.Repeat("x", 4096)
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{{Type: "file", Path: "large.txt"}}, 64); err == nil {
		t.Fatal("insufficient budget must fail instead of sending only a path")
	}
}

func TestResolveReferenceBinarySkipped(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	if err := os.WriteFile(filepath.Join(dir, "image.bin"), []byte{0x00, 0x01, 0x02}, 0600); err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{{Type: "file", Path: "image.bin"}}, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resolution.prompt, "image.bin") && !strings.Contains(resolution.prompt, "unavailable") {
		t.Fatalf("binary file must be explicit: %s", resolution.prompt)
	}
}

// 目录引用必须把来源、manifest 与实际正文一起交给模型，而不是仅发送路径。
func TestChatDirectoryReferenceReachesModelWithSources(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "main.go"), []byte("package main // directory-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := client.KaguyaProject.Create().SetName("refs").SetPath(dir).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body.Messages), "directory-marker") || !strings.Contains(string(body.Messages), "Referenced directory") {
			t.Errorf("directory content or source missing: %s", body.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{
		ProjectID: p.ID, ModelID: model.ID, Messages: "inspect",
		References: []dtochat.Reference{{Type: "directory", Path: "pkg"}},
	})
	done := false
	var doneSummary *dtochat.TurnReferenceSummary
	var conversationID string
	for frame := range frames {
		if frame.Err != nil {
			t.Fatal(frame.Err)
		}
		if frame.Chat.ID != "" {
			conversationID = frame.Chat.ID
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			done = true
			doneSummary = frame.ReferenceSummary
			committed, err := (&AgentSvc{}).ConversationTurns(ctx, frame.Chat.ID, &dtochat.TurnPageReq{Limit: 5})
			if err != nil || len(committed.Items) != 1 || committed.Items[0].Status != "completed" || committed.Items[0].ReferenceSummary == nil {
				t.Fatalf("done precedes committed summary: %+v err=%v", committed, err)
			}
		}
	}
	if !done {
		t.Fatal("missing done frame")
	}
	// 轮次摘要持久化路径与范围，不保存第二份正文。
	turns, err := (&AgentSvc{}).ConversationTurns(ctx, conversationID, &dtochat.TurnPageReq{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.Items) != 1 || turns.Items[0].ReferenceSummary == nil {
		t.Fatalf("reference summary not persisted: %+v", turns.Items)
	}
	if len(turns.Items[0].ReferenceSummary.Sources) != 1 || turns.Items[0].ReferenceSummary.Sources[0].Path != "pkg" {
		t.Fatalf("summary sources = %+v", turns.Items[0].ReferenceSummary)
	}
	doneJSON, err := json.Marshal(doneSummary)
	if err != nil {
		t.Fatal(err)
	}
	historyJSON, err := json.Marshal(turns.Items[0].ReferenceSummary)
	if err != nil {
		t.Fatal(err)
	}
	if doneSummary == nil || string(doneJSON) != string(historyJSON) {
		t.Fatalf("done/history differ: done=%s history=%s", doneJSON, historyJSON)
	}
	if doneSummary.PreparationState != "resolved" {
		t.Fatalf("done summary state = %q", doneSummary.PreparationState)
	}
}

// 普通会话不允许引用，即使 toolset 存在（~/.kaguya）。
func TestChatOrdinaryConversationRejectsReferences(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("model must not be called when references are invalid")
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
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{
		ModelID: model.ID, Messages: "inspect",
		References: []dtochat.Reference{{Type: "file", Path: "note.txt"}},
	})
	var got error
	for frame := range frames {
		if frame.Err != nil {
			got = frame.Err
		}
	}
	if got == nil || !errors.Is(got, ErrReferenceInvalid) {
		t.Fatalf("ordinary conversation reference error = %v", got)
	}
	if count, err := client.KaguyaConversation.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("invalid references created conversation: count=%d err=%v", count, err)
	}
	if count, err := client.KaguyaTitleJob.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("invalid references created title job: count=%d err=%v", count, err)
	}
}

// 显式文件与目录重叠时内容只装入一次：显式文件优先，目录清单标记已包含。
func TestResolveReferenceOverlapReadsOnce(t *testing.T) {
	ctx, projectID, dir := seedReferenceProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.go"), []byte("package sub // overlap-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveReferenceContext(ctx, projectID, []dtochat.Reference{
		{Type: "directory", Path: "sub"},
		{Type: "file", Path: "sub/nested.go"},
	}, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(resolution.prompt, "overlap-marker"); count != 1 {
		t.Fatalf("overlapping content must be included once, got %d: %s", count, resolution.prompt)
	}
	if !strings.Contains(resolution.prompt, "[already included from another reference]") {
		t.Fatalf("directory manifest must mark the overlap: %s", resolution.prompt)
	}
	// 显式文件来源排在目录来源之前，摘要顺序反映实际装入顺序。
	if len(resolution.summary.Sources) != 2 || resolution.summary.Sources[0].Type != "file" {
		t.Fatalf("summary order = %+v", resolution.summary.Sources)
	}
}
