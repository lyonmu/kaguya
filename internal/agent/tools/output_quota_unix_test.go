//go:build darwin || linux

package tools

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/global"
)

func TestBashConversationOutputQuotaStopsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	s := setup(t)
	oldID := global.Id
	global.Id = &testIDGenerator{next: 4234}
	t.Cleanup(func() { global.Id = oldID })
	dir := seedConversationOutputQuota(t, s, 4096)
	started := time.Now()
	response := run(t, s.BashTool(), BashInput{Command: `printf '%s\n' "$$" > quota-leader.pid; (sleep 30; printf leaked > quota-leaked-child) & for ((i=0;i<10000;i++)); do printf 'boundary-resource\n'; done; wait`})
	if !strings.Contains(response.Content, "Output limit reached") || time.Since(started) > 10*time.Second {
		t.Fatal("conversation quota did not stop the real command promptly")
	}
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(metadata.Path)
	if err != nil || info.Size() != 4096 || conversationOutputSize(t, dir) != maxConversationOutputBytes {
		t.Fatal("real command did not preserve only the available prefix", err)
	}
	page := run(t, s.ReadTool(), ReadInput{Path: metadata.Path})
	requireOK(t, page)
	if !strings.Contains(page.Content, "boundary-resource") {
		t.Fatal("saved output is not readable by its conversation")
	}
	data, err := os.ReadFile(filepath.Join(s.cwd, "quota-leader.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatal("missing real process-group identity", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("quota command process group did not exit", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(s.cwd, "quota-leaked-child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quota cancellation left a child side effect", err)
	}
	outputQuotas.Lock()
	retained := len(outputQuotas.entries)
	outputQuotas.Unlock()
	if retained != 0 {
		t.Fatal("real command retained quota owners")
	}
}

func TestBashOutputCaptureFailureStopsCommand(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	s := setup(t)
	if err := s.tempRoot.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := run(t, s.BashTool(), BashInput{Command: `for ((i=0;i<10000;i++)); do printf 'capture-failure-resource\n'; done; sleep 30; printf leaked > capture-leaked`})
	if !response.IsError || !strings.Contains(response.Content, "save command output") || time.Since(started) > 10*time.Second {
		t.Fatal("capture failure did not clearly fail and stop the command promptly")
	}
	if _, err := os.Stat(filepath.Join(s.cwd, "capture-leaked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capture failure allowed a later command side effect", err)
	}
	outputQuotas.Lock()
	retained := len(outputQuotas.entries)
	outputQuotas.Unlock()
	if retained != 0 {
		t.Fatal("capture failure retained quota owners")
	}
}
