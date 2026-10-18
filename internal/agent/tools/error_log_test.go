package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestReadErrorKeepsResultButDoesNotLogInput(t *testing.T) {
	const marker = "fake-secret-missing-file"
	core, logs := observer.New(zap.DebugLevel)
	set, err := New(t.TempDir(), "safe-error-log", zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	input, _ := json.Marshal(ReadInput{Path: marker})
	response, err := set.ReadTool().Run(context.Background(), fantasy.ToolCall{ID: "safe-read", Name: "read", Input: string(input)})
	if err != nil || !response.IsError || !strings.Contains(response.Content, marker) {
		t.Fatal("actual read must retain its actionable path error for the caller", err)
	}
	failed := logs.FilterMessage("tool failed").All()
	if len(failed) != 1 || failed[0].ContextMap()["tool"] != "read" || failed[0].ContextMap()["call_id"] != "safe-read" {
		t.Fatal("failed tool lost its non-sensitive diagnostic identity")
	}
	for _, entry := range logs.All() {
		fields, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message+string(fields), marker) {
			t.Fatal("actual file error leaked the input path to application logs")
		}
	}
}
