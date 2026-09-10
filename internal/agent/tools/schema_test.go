package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func TestToolExamplesMatchPublishedSchemas(t *testing.T) {
	s := setup(t)
	for _, tool := range s.AllTools() {
		t.Run(tool.Info().Name, func(t *testing.T) {
			info := tool.Info()
			_, example, ok := strings.Cut(info.Description, "Example: ")
			if !ok {
				t.Fatal("missing argument example")
			}
			var input any
			if err := json.NewDecoder(strings.NewReader(example)).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if result := tool.(*inputContractTool).validator.Validate(input); !result.IsValid() {
				t.Fatalf("invalid example: %v", result.DetailedErrors())
			}
			for field, parameter := range info.Parameters {
				if parameter.(map[string]any)["description"] == nil {
					t.Errorf("%s has no field description", field)
				}
			}
			// A provider mutating one Info tree must not alter later requests or validation.
			for _, parameter := range info.Parameters {
				parameter.(map[string]any)["type"] = "null"
			}
			for _, parameter := range tool.Info().Parameters {
				if parameter.(map[string]any)["type"] == "null" {
					t.Fatal("shared mutable schema")
				}
			}
		})
	}
}

func TestInputContractRejectsMistakesBeforeExecution(t *testing.T) {
	s := setup(t)
	path := filepath.Join(s.cwd, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		tool         fantasy.AgentTool
		input, field string
	}{
		{"unknown flag", s.WriteTool(), `{"path":"file.txt","content":"after","append":true}`, "append"},
		{"missing content", s.WriteTool(), `{"path":"file.txt"}`, "content"},
		{"empty path", s.WriteTool(), `{"path":"","content":"after"}`, "path"},
		{"zero line", s.ReadTool(), `{"path":"file.txt","offset":0}`, "offset"},
		{"string line", s.ReadTool(), `{"path":"file.txt","offset":"1"}`, "offset"},
		{"null optional", s.ReadTool(), `{"path":"file.txt","limit":null}`, "limit"},
		{"empty edits", s.EditTool(), `{"path":"file.txt","edits":[]}`, "edits"},
		{"missing replacement", s.EditTool(), `{"path":"file.txt","edits":[{"oldText":"before"}]}`, "newText"},
		{"empty match", s.EditTool(), `{"path":"file.txt","edits":[{"oldText":"","newText":"after"}]}`, "oldText"},
		{"nested unknown flag", s.EditTool(), `{"path":"file.txt","edits":[{"oldText":"before","newText":"after","replace_all":true}]}`, "replace_all"},
		{"wrong command key", s.BashTool(), `{"cmd":"touch should-not-exist"}`, "command"},
		{"blank command", s.BashTool(), `{"command":"   "}`, "command"},
		{"zero timeout", s.BashTool(), `{"command":"touch should-not-exist","timeout":0}`, "timeout"},
		{"search limit", s.FindTool(), `{"pattern":"*.go","limit":100001}`, "limit"},
		{"search context", s.GrepTool(), `{"pattern":"x","context":-1}`, "context"},
		{"not an object", s.WriteTool(), `[]`, "object"},
		{"malformed JSON", s.WriteTool(), `{"path":`, "JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.tool.Run(context.Background(), fantasy.ToolCall{ID: "provider-call", Name: tc.tool.Info().Name, Input: tc.input})
			if err != nil || !r.IsError || !strings.Contains(r.Content, "invalid parameters") || !strings.Contains(r.Content, tc.field) {
				t.Fatalf("response=%+v err=%v", r, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "before" {
				t.Fatal("invalid arguments mutated file")
			}
			if _, err := os.Stat(filepath.Join(s.cwd, "should-not-exist")); !os.IsNotExist(err) {
				t.Fatal("invalid arguments ran shell")
			}
		})
	}
	// Empty newText is intentional deletion, not a missing argument.
	requireOK(t, run(t, s.EditTool(), map[string]any{"path": "file.txt", "edits": []any{map[string]any{"oldText": "before", "newText": ""}}}))
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("empty replacement rejected: %q %v", data, err)
	}
}
