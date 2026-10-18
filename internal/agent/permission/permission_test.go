package permission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/toolcontract"
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
		{ModeFullAccess, Classification{Effect: EffectUnknown}, DecisionAllow},
		{"invalid", Classification{Effect: EffectReadOnly}, DecisionRequireApproval},
	}
	for _, tc := range cases {
		if got := Decide(tc.mode, tc.class); got != tc.want {
			t.Errorf("Decide(%q, %+v) = %q, want %q", tc.mode, tc.class, got, tc.want)
		}
	}
}

func TestShellClassificationRequiresApprovalForEveryCommand(t *testing.T) {
	commands := []string{
		"pwd", "true", "  pwd ", "", "pwd -P", "'pwd'", `"pwd"`, "pwd > out", "pwd | cat",
		"true && rm -rf x", "echo $(pwd)", "X=1 pwd", "pwd; true", "! pwd", "pwd &",
		"cat README.md", "git status --short", "go test ./...", "rm -rf build",
		"find . -exec ls {} +", "bash -c pwd", "eval pwd", "f() { pwd; }; f",
	}
	for _, command := range commands {
		class := ShellClassification(command)
		if class.Identity != "builtin:bash" || class.Effect != EffectUnknown {
			t.Errorf("ShellClassification(%q) = %+v, want unknown", command, class)
		}
		for _, mode := range []Mode{ModeAutoApproval, ModeFullApproval} {
			if Decide(mode, class) != DecisionRequireApproval {
				t.Errorf("command %q bypasses %s", command, mode)
			}
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

func TestWrapperAutoApprovalBashDoesNotRunWithoutApproval(t *testing.T) {
	tool := &fakeTool{name: "bash"}
	approver := &fakeApprover{outcome: OutcomeRejected}
	wrapper := &Wrapper{
		tool: tool, class: ShellClassification("pwd"), mode: ModeAutoApproval, approver: approver,
	}
	response, err := wrapper.Run(context.Background(), fantasy.ToolCall{ID: "call-3", Input: `{"command":"pwd"}`})
	if err != nil || approver.calls != 1 || tool.runs != 0 || !response.IsError {
		t.Fatalf("bash must wait for approval: calls=%d runs=%d response=%+v err=%v", approver.calls, tool.runs, response, err)
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

func TestWrapperInvalidSchemaDoesNotRegisterApproval(t *testing.T) {
	for _, input := range []string{`[]`, `null`, `{"path":`, `{}`, `{"path":"file","extra":true}`} {
		t.Run(input, func(t *testing.T) {
			original := &fakeTool{name: "write"}
			tool := toolcontract.WrapGenerated[struct {
				Path string `json:"path"`
			}](original)
			approver := &fakeApprover{outcome: OutcomeApproved}
			wrapper := &Wrapper{tool: tool, class: Classification{Effect: EffectMutating}, mode: ModeFullApproval, approver: approver}
			response, err := wrapper.Run(context.Background(), fantasy.ToolCall{Input: input})
			if err != nil || !response.IsError || original.runs != 0 || approver.calls != 0 {
				t.Fatalf("invalid schema reached approval/execution: response=%+v err=%v runs=%d approvals=%d", response, err, original.runs, approver.calls)
			}
		})
	}
}

type callbackApprover func(context.Context, *Request) (Outcome, error)

func (a callbackApprover) Await(ctx context.Context, req *Request) (Outcome, error) {
	return a(ctx, req)
}

func TestWrapperCancellationBeforeAndAfterApprovalDoesNotRun(t *testing.T) {
	for _, alreadyCanceled := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		tool := &fakeTool{name: "write"}
		approvals := 0
		approver := callbackApprover(func(context.Context, *Request) (Outcome, error) {
			approvals++
			cancel()
			return OutcomeApproved, nil
		})
		if alreadyCanceled {
			cancel()
		}
		wrapper := &Wrapper{tool: tool, class: Classification{Effect: EffectMutating}, mode: ModeFullApproval, approver: approver}
		_, err := wrapper.Run(ctx, fantasy.ToolCall{Input: `{}`})
		cancel()
		if !errors.Is(err, context.Canceled) || tool.runs != 0 || (alreadyCanceled && approvals != 0) {
			t.Fatalf("canceled call ran: err=%v runs=%d approvals=%d", err, tool.runs, approvals)
		}
	}
}

func TestWrapAllFullAccessPreservesOriginalTool(t *testing.T) {
	tool := &fakeTool{name: "bash"}
	wrapped := WrapAll(ModeFullAccess, nil, []GatedTool{{Tool: tool, Class: ShellClassification("pwd")}})
	if len(wrapped) != 1 || wrapped[0] != tool {
		t.Fatal("full_access must retain original tool")
	}
}
