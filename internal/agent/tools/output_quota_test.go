package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

func seedConversationOutputQuota(t *testing.T, s *Set, remaining int64) string {
	t.Helper()
	dir := filepath.Join(s.tempBase, s.conversationOutputDir())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "bash-987654321.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// 配额按逻辑文件长度计数；默认回归用sparse fixture，不写256MiB正文。
	err = file.Truncate(maxConversationOutputBytes - remaining)
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	return dir
}

func conversationOutputSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

func TestBashOutputRespectsRemainingConversationQuota(t *testing.T) {
	s := setup(t)
	oldID := global.Id
	global.Id = &testIDGenerator{next: 1234}
	t.Cleanup(func() { global.Id = oldID })
	const remaining = 1024
	dir := seedConversationOutputQuota(t, s, remaining)
	var stopped atomic.Int64
	output := &outputAccumulator{s: s, onQuota: func() { stopped.Add(1) }}
	t.Cleanup(func() { _, _ = output.finish() })
	prefix := bytes.Repeat([]byte("p\n"), 1000)
	if _, err := output.Write(prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write(bytes.Repeat([]byte("q\n"), 30000)); err != nil {
		t.Fatal(err)
	}
	// stdout拷贝可能在cancel后继续交付已缓冲的块；不能再保存或再次cancel。
	if _, err := output.Write([]byte("post-quota-tail\n")); err != nil {
		t.Fatal(err)
	}
	response, err := output.finish()
	if err != nil {
		t.Fatal(err)
	}
	if total := conversationOutputSize(t, dir); total != maxConversationOutputBytes {
		t.Fatalf("conversation quota exceeded or prefix lost: size=%d limit=%d", total, maxConversationOutputBytes)
	}
	if stopped.Load() != 1 || !strings.Contains(response.Content, "Output limit reached") || !strings.Contains(response.Content, "post-quota-tail") {
		t.Fatal("quota stop/tail was not reported once")
	}
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(metadata.Path)
	if err != nil || !bytes.Equal(data, prefix[:remaining]) {
		t.Fatal("saved output did not retain the exact available prefix", err)
	}
}

func TestBashConcurrentOutputSetsShareDirectoryQuota(t *testing.T) {
	s := setup(t)
	oldID := global.Id
	global.Id = &testIDGenerator{next: 2234}
	t.Cleanup(func() { global.Id = oldID })
	alias := filepath.Join(t.TempDir(), "output-alias")
	if err := os.Symlink(s.tempBase, alias); err != nil {
		t.Fatal(err)
	}
	other, err := newSet(s.cwd, s.conversationID, alias, s.outputDate, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	dir := seedConversationOutputQuota(t, s, 8000)
	var stopped atomic.Int64
	outputs := []*outputAccumulator{
		{s: s, onQuota: func() { stopped.Add(1) }},
		{s: other, onQuota: func() { stopped.Add(1) }},
	}
	t.Cleanup(func() {
		for _, output := range outputs {
			_, _ = output.finish()
		}
	})
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, len(outputs))
	for _, output := range outputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := output.Write(bytes.Repeat([]byte("x\n"), 3000))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
	for _, output := range outputs {
		if _, err := output.finish(); err != nil {
			t.Error(err)
		}
	}
	if total := conversationOutputSize(t, dir); total != maxConversationOutputBytes || stopped.Load() != 1 {
		t.Fatalf("parallel aliases did not share the remaining quota: size=%d stopped=%d", total, stopped.Load())
	}
	outputQuotas.Lock()
	retained := len(outputQuotas.entries)
	outputQuotas.Unlock()
	if retained != 0 {
		t.Fatal("finished outputs retained quota owners")
	}
}

func TestBashOutputQuotaRecountsAfterWriterGeneration(t *testing.T) {
	s := setup(t)
	oldID := global.Id
	global.Id = &testIDGenerator{next: 3234}
	t.Cleanup(func() { global.Id = oldID })
	dir := seedConversationOutputQuota(t, s, 6000)
	var stopped atomic.Int64
	first := &outputAccumulator{s: s, onQuota: func() { stopped.Add(1) }}
	t.Cleanup(func() { _, _ = first.finish() })
	if _, err := first.Write(bytes.Repeat([]byte("x\n"), 3000)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.finish(); err != nil {
		t.Fatal(err)
	}
	second := &outputAccumulator{s: s, onQuota: func() { stopped.Add(1) }}
	t.Cleanup(func() { _, _ = second.finish() })
	if _, err := second.Write(bytes.Repeat([]byte("y\n"), 3000)); err != nil {
		t.Fatal(err)
	}
	response, err := second.finish()
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Path != "" || stopped.Load() != 2 || !strings.Contains(response.Content, "y\ny\n") || conversationOutputSize(t, dir) != maxConversationOutputBytes {
		t.Fatal("a later writer did not recount the full directory or lost its tail")
	}
	if _, err := second.Write([]byte("after finish")); !errors.Is(err, os.ErrClosed) {
		t.Fatal("finished writer accepted new bytes", err)
	}
	outputQuotas.Lock()
	retained := len(outputQuotas.entries)
	outputQuotas.Unlock()
	if retained != 0 {
		t.Fatal("full directory retained a quota owner")
	}
}
