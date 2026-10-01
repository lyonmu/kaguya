package permission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"
)

type fakeTool struct {
	name  string
	runs  int
	reply string
}

func (f *fakeTool) Info() fantasy.ToolInfo { return fantasy.ToolInfo{Name: f.name} }
func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{}
}
func (f *fakeTool) SetProviderOptions(fantasy.ProviderOptions) {}
func (f *fakeTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	f.runs++
	return fantasy.NewTextResponse(f.reply), nil
}

type fakeApprover struct {
	outcome Outcome
	calls   int
}

func (a *fakeApprover) Await(_ context.Context, req *Request) (Outcome, error) {
	a.calls++
	req.ApprovalID = "approval-1"
	return a.outcome, nil
}

func TestDecidePolicyMatrix(t *testing.T) {
	cases := []struct {
		mode  Mode
		class Classification
		want  Decision
	}{
		{ModeFullApproval, Classification{Effect: EffectReadOnly}, DecisionRequireApproval},
		{ModeFullApproval, Classification{Effect: EffectMutating}, DecisionRequireApproval},
		{ModeFullApproval, Classification{Effect: EffectUnknown}, DecisionRequireApproval},
		{ModeAutoApproval, Classification{Effect: EffectReadOnly}, DecisionAllow},
		{ModeAutoApproval, Classification{Effect: EffectMutating}, DecisionRequireApproval},
		{ModeAutoApproval, Classification{Effect: EffectUnknown}, DecisionRequireApproval},
		{ModeAutoApproval, Classification{Effect: EffectUnknown, ReadOnlyProven: true}, DecisionAllow},
		{ModeFullAccess, Classification{Effect: EffectUnknown}, DecisionAllow},
		{"invalid", Classification{Effect: EffectReadOnly}, DecisionRequireApproval},
	}
	for _, tc := range cases {
		if got := Decide(tc.mode, tc.class); got != tc.want {
			t.Errorf("Decide(%q, %+v) = %q, want %q", tc.mode, tc.class, got, tc.want)
		}
	}
}

func TestShellClassificationOnlyProvenLiteralCommands(t *testing.T) {
	allowed := []string{"pwd", "true", "  pwd "}
	for _, command := range allowed {
		class := ShellClassification(command)
		if !class.ReadOnlyProven || class.Effect != EffectReadOnly {
			t.Errorf("ShellClassification(%q) = %+v, want read-only", command, class)
		}
	}
	denied := []string{
		"", "pwd -P", "'pwd'", `"pwd"`, "pwd > out", "pwd | cat", "true && rm -rf x",
		"echo $(pwd)", "X=1 pwd", "pwd; true", "! pwd", "pwd &", "cat README.md",
		"git status --short", "git diff", "go test ./...", "rm -rf build",
		"find . -exec ls {} +", "bash -c pwd", "eval pwd", "f() { pwd; }; f",
	}
	for _, command := range denied {
		class := ShellClassification(command)
		if class.Effect != EffectUnknown || class.ReadOnlyProven {
			t.Errorf("ShellClassification(%q) = %+v, want unknown approval", command, class)
		}
	}
}

func TestWrapperApprovedRunsOriginalTool(t *testing.T) {
	tool := &fakeTool{name: "bash", reply: "ok"}
	approver := &fakeApprover{outcome: OutcomeApproved}
	wrapper := &Wrapper{tool: tool, class: Classification{Identity: "builtin:bash", Effect: EffectUnknown}, mode: ModeFullApproval, approver: approver}
	response, err := wrapper.Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"pwd"}`})
	if err != nil {
		t.Fatal(err)
	}
	if tool.runs != 1 || response.IsError {
		t.Fatalf("approved call must run once: runs=%d response=%+v", tool.runs, response)
	}
	if approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", approver.calls)
	}
}

func TestWrapperRejectedDoesNotRunAndCarriesMetadata(t *testing.T) {
	tool := &fakeTool{name: "write"}
	approver := &fakeApprover{outcome: OutcomeRejected}
	wrapper := &Wrapper{tool: tool, class: Classification{Identity: "builtin:write", Effect: EffectMutating}, mode: ModeFullApproval, approver: approver}
	response, err := wrapper.Run(context.Background(), fantasy.ToolCall{ID: "call-2", Name: "write", Input: `{"path":"a"}`})
	if err != nil {
		t.Fatal(err)
	}
	if tool.runs != 0 {
		t.Fatalf("rejected call must not run: runs=%d", tool.runs)
	}
	if !response.IsError {
		t.Fatalf("rejected call must be an error result: %+v", response)
	}
	var meta Metadata
	if err := json.Unmarshal([]byte(response.Metadata), &meta); err != nil {
		t.Fatalf("metadata must be valid JSON: %v", err)
	}
	if meta.Outcome != string(OutcomeRejected) || meta.ApprovalID != "approval-1" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestWrapperDynamicClassificationAllowsProvenReadOnly(t *testing.T) {
	tool := &fakeTool{name: "bash"}
	approver := &fakeApprover{outcome: OutcomeRejected}
	wrapper := &Wrapper{
		tool: tool, class: Classification{Identity: "builtin:bash", Effect: EffectUnknown},
		mode: ModeAutoApproval, approver: approver, classify: func(call fantasy.ToolCall) Classification { return ShellClassification(call.Input) },
	}
	response, err := wrapper.Run(context.Background(), fantasy.ToolCall{ID: "call-3", Input: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if approver.calls != 0 || tool.runs != 1 || response.IsError {
		t.Fatalf("proven read-only must bypass approval: calls=%d runs=%d response=%+v", approver.calls, tool.runs, response)
	}
}

func TestWrapperInfoDelegatesToOriginalTool(t *testing.T) {
	tool := &fakeTool{name: "read"}
	wrapper := &Wrapper{tool: tool, class: Classification{Effect: EffectReadOnly}, mode: ModeFullApproval}
	if wrapper.Info().Name != "read" {
		t.Fatalf("wrapper Info must delegate: %+v", wrapper.Info())
	}
}

func TestHashInputStable(t *testing.T) {
	if HashInput(`{"a":1}`) != HashInput(`{"a":1}`) {
		t.Fatal("hash must be deterministic")
	}
	if HashInput(`{"a":1}`) == HashInput(`{"a":2}`) {
		t.Fatal("hash must change with input")
	}
}

func TestApproverErrorPropagates(t *testing.T) {
	tool := &fakeTool{name: "write"}
	wrapper := &Wrapper{tool: tool, class: Classification{Effect: EffectMutating}, mode: ModeFullApproval, approver: errorApprover{}}
	if _, err := wrapper.Run(context.Background(), fantasy.ToolCall{ID: "call-4"}); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("approver error must abort the call, got %v", err)
	}
	if tool.runs != 0 {
		t.Fatal("tool must not run when approval persistence fails")
	}
}

type errorApprover struct{}

func (errorApprover) Await(context.Context, *Request) (Outcome, error) {
	return "", errors.New("db down")
}
