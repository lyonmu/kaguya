//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
	"github.com/lyonmu/kaguya/internal/global"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
	"go.uber.org/zap"
)

var referenceResource = flag.Bool("reference-resource", false, "measure three rounds of physical 10k/100k directory references and project instructions")

type referenceResourceResult struct {
	Phase          string              `json:"phase"`
	Round          int                 `json:"round"`
	PhysicalFiles  int                 `json:"physical_small_files"`
	PhysicalBytes  int64               `json:"physical_small_bytes"`
	ElapsedMS      float64             `json:"elapsed_ms"`
	Before         titleResourceSample `json:"before"`
	ObservedPeak   titleResourceSample `json:"observed_peak"`
	AfterGC        titleResourceSample `json:"after_gc"`
	TotalAllocated uint64              `json:"total_allocated_bytes"`
	OutputBytes    int                 `json:"output_bytes"`
	MetadataBytes  int64               `json:"metadata_bytes,omitempty"`
	Candidates     int                 `json:"candidates,omitempty"`
	Pages          int                 `json:"pages,omitempty"`
	PartialReason  string              `json:"partial_reason,omitempty"`
}

// 复用标题负载的仪器，不给生产链加入测量hook。heap每1ms、RSS/FD每100ms
// 采样并包含操作前/持有输出时的端点；这是观测最大值，不是连续精确峰值。
func measureReferenceResource(t *testing.T, result *referenceResourceResult, operation func() any) {
	t.Helper()
	runtime.GC()
	result.Before = sampleTitleResources(t)
	result.ObservedPeak = result.Before
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var mu sync.Mutex
	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				mu.Lock()
				result.ObservedPeak.HeapBytes = max(result.ObservedPeak.HeapBytes, memory.HeapAlloc)
				mu.Unlock()
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sample := sampleTitleResources(t)
				mu.Lock()
				result.ObservedPeak.include(sample)
				mu.Unlock()
			}
		}
	}()
	var stopOnce sync.Once
	finish := func() { stopOnce.Do(func() { close(stop); workers.Wait() }) }
	defer finish() // Fatal也先join仪器，再由fixture关闭DB/临时目录。
	started := time.Now()
	retained := operation()
	result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	endpoint := sampleTitleResources(t)
	finish()
	result.ObservedPeak.include(endpoint)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	result.TotalAllocated = after.TotalAlloc - before.TotalAlloc
	runtime.KeepAlive(retained)
	retained = nil
	runtime.GC()
	result.AfterGC = sampleTitleResources(t)
	if result.AfterGC.FDs != result.Before.FDs {
		t.Errorf("%s leaked descriptors: before=%d after=%d", result.Phase, result.Before.FDs, result.AfterGC.FDs)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("REFERENCE_RESOURCE %s", encoded)
}

func TestPhysicalReferenceResources(t *testing.T) {
	if !*referenceResource {
		t.Skip("run with -reference-resource to collect the physical release resource gate")
	}
	for _, count := range []int{10000, 100000} {
		t.Run(fmt.Sprintf("files_%d", count), func(t *testing.T) {
			// 与生产相同的单连接加密磁盘SQLCipher，只有假key与隔离HOME。
			client := openDiskChatClient(t)
			oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
			gen := &chatTestID{}
			gen.value.Store(123456789012340)
			db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
			t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, "project")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			body := []byte(strings.Repeat("resource-file-body\n", 56) + "tail\n")
			for index := 0; index < count; index++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("small-%06d.txt", index)), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// 真正的十万无关根目录项；项目指令仍需枚举全部名字，并保留ignore
			// 不限制项目AGENTS的既有合同。文件创建不计入执行耗时/内存。
			instructionPath := filepath.Join(dir, "AGENTS.md")
			header := fmt.Sprintf("\n\nProject instructions from %s:\n", instructionPath)
			instructionBody := strings.Repeat("x", 256*1024-len(header))
			if err := os.WriteFile(instructionPath, []byte(instructionBody), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("AGENTS.md\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			project, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "resource", Path: dir})
			if err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 3; round++ {
				newResult := func(phase string) *referenceResourceResult {
					return &referenceResourceResult{Phase: phase, Round: round, PhysicalFiles: count, PhysicalBytes: int64(count * len(body))}
				}
				search := newResult("candidate_pages")
				measureReferenceResource(t, search, func() any {
					req := &dtoproject.FilesReq{ID: project.ID, IncludeDirectories: true}
					seen := map[string]bool{}
					for {
						page, err := (&projectsvc.ProjectSvc{}).FilesPage(ctx, req)
						if err != nil || page == nil {
							t.Fatalf("candidate page: %v", err)
						}
						search.Pages++
						if page.CursorReset || len(page.Items) > 50 || !page.ScanIncomplete || !page.Truncated {
							t.Fatalf("invalid bounded candidate page: %+v", page)
						}
						for _, item := range page.Items {
							if seen[item.Path] || item.Path == "AGENTS.md" {
								t.Fatalf("duplicate/filtered candidate: %q", item.Path)
							}
							seen[item.Path] = true
						}
						search.Candidates += len(page.Items)
						if page.NextCursor == "" {
							if search.Candidates < 51 || !seen["."] {
								t.Fatalf("pagination failed to advance: %+v", search)
							}
							return page
						}
						if page.NextCursor == req.Cursor || search.Pages > 100 {
							t.Fatal("bounded observed collection did not reach a terminal partial page")
						}
						req.Cursor = page.NextCursor
					}
				})
				resolver := newResult("resolver")
				measureReferenceResource(t, resolver, func() any {
					resolved, err := resolveReferenceContext(ctx, project.ID, []dtochat.Reference{{Type: "directory", Path: "."}}, 64<<10)
					if err != nil {
						t.Fatal(err)
					}
					checkResourceReference(t, resolved, resolver)
					return resolved
				})
				ledger := newResult("formatter_ledger")
				measureReferenceResource(t, ledger, func() any {
					root, err := workspace.Open(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer root.Close()
					scanCtx, stop := context.WithTimeout(ctx, referenceScanBudget)
					defer stop()
					matcher, err := workspace.NewIgnoreMatcher(scanCtx, root)
					if err != nil {
						t.Fatal(err)
					}
					reader := &referenceReader{ctx: ctx, scanCtx: scanCtx, filter: workspace.NewReferenceFilter(scanCtx, root, matcher), budget: 64 << 10, metadata: workspace.ReferenceMetadataBudget{Limit: referenceMetadataBytes}}
					if err := reader.addSource(dtochat.Reference{Type: "directory", Path: "."}); err != nil {
						t.Fatal(err)
					}
					resolved, err := reader.format() // 保留生产原语，未替换readWindow。
					if err != nil {
						t.Fatal(err)
					}
					checkResourceReference(t, resolved, ledger)
					ledger.MetadataBytes = reader.metadata.Used
					if reader.metadata.Used < 0 || reader.metadata.Used > referenceMetadataBytes {
						t.Fatalf("metadata ledger: %+v", reader.metadata)
					}
					for _, entry := range reader.cache {
						if entry.text != "" {
							t.Fatal("cache retained a second body")
						}
					}
					return resolved
				})
				instructions := newResult("project_instructions")
				measureReferenceResource(t, instructions, func() any {
					text, err := loadProjectInstructions(ctx, dir)
					if err != nil || text != header+instructionBody || len(text) != 256*1024 {
						t.Fatalf("physical root instructions: bytes=%d err=%v", len(text), err)
					}
					instructions.OutputBytes = len(text)
					return text
				})
			}
			// 在实际大目录读取首个128项后取消，不能返回部分指令名或继续读。
			file, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			canceled, stop := context.WithCancel(ctx)
			calls := 0
			names, err := projectInstructionNames(canceled, func(limit int) ([]os.DirEntry, error) {
				calls++
				entries, err := file.ReadDir(limit)
				stop()
				return entries, err
			})
			if !errors.Is(err, context.Canceled) || names != nil || calls != 1 {
				t.Fatalf("physical directory cancel: names=%v calls=%d err=%v", names, calls, err)
			}
			if resolved, err := resolveReferenceContext(canceled, project.ID, []dtochat.Reference{{Type: "directory", Path: "."}}, 64<<10); resolved != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("resolver cancel: result=%+v err=%v", resolved, err)
			}
		})
	}
}

func checkResourceReference(t *testing.T, resolved *referenceResolution, result *referenceResourceResult) {
	t.Helper()
	if resolved == nil || resolved.summary == nil {
		t.Fatal("missing reference resolution")
	}
	summary := resolved.summary
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > referenceMetadataBytes || len(resolved.prompt) > 64<<10 || summary.InlineBytes != int64(len(resolved.prompt)) || !utf8.ValidString(resolved.prompt) {
		t.Fatalf("reference output exceeded actual budget: inline=%d metadata=%d err=%v", len(resolved.prompt), len(encoded), err)
	}
	if !summary.ScanIncomplete || len(summary.Continuation) != 1 || len(summary.Sources) != 1 || !summary.Sources[0].Included || len(summary.Sources[0].Files) == 0 {
		t.Fatalf("large directory was falsely complete or bodyless: %+v", summary)
	}
	result.OutputBytes, result.PartialReason = len(resolved.prompt), summary.PartialReason
	result.Candidates = len(summary.Sources[0].Files)
}

func TestPhysicalLongLineAndSpecialResources(t *testing.T) {
	if !*referenceResource {
		t.Skip("run with -reference-resource to collect the physical release resource gate")
	}
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "long.txt"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 64<<10)
	for index := 0; index < 256; index++ {
		if _, err := io.WriteString(file, chunk); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside data must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		result := &referenceResourceResult{Phase: "16mib_single_line_and_special", Round: round}
		measureReferenceResource(t, result, func() any {
			root, err := workspace.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx, cancel := context.WithTimeout(context.Background(), referenceScanBudget)
			defer cancel()
			window, err := workspace.ReadTextWindow(ctx, root, "long.txt", 0, referenceLineLimit, referenceMaxFileBytes, referenceLineByteLimit, referenceLineByteLimit)
			if err != nil || !window.OversizedLine || !window.Truncated || window.Grown || len(window.Content) != 0 || window.Lines != 1 {
				t.Fatalf("single large line: %+v err=%v", window, err)
			}
			for _, name := range []string{"pipe", "outside"} {
				if _, err := workspace.ReadTextWindow(ctx, root, name, 0, 1, referenceMaxFileBytes, referenceLineByteLimit, referenceLineByteLimit); err == nil {
					t.Fatalf("special/escaping path accepted: %s", name)
				}
			}
			return window
		})
	}
}
