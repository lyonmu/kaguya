package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/agent/tools"
	"go.uber.org/zap"
)

type identityTool struct {
	fantasy.AgentTool
	identity string
}

func (t identityTool) PermissionIdentity() string { return t.identity }

type rejectingApprover struct{ calls int }

func (a *rejectingApprover) Await(context.Context, *permission.Request) (permission.Outcome, error) {
	a.calls++
	return permission.OutcomeRejected, nil
}

func TestToolClassificationTrustsRegistrationRatherThanName(t *testing.T) {
	for _, name := range []string{"read", "write", "bash", "memory_read"} {
		base := fantasy.NewAgentTool(name, "", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			t.Fatal("classification must not execute a tool")
			return fantasy.ToolResponse{}, nil
		})
		for _, tool := range []fantasy.AgentTool{base, identityTool{AgentTool: base, identity: "mcp:server:read:1:schema"}} {
			class := classifyRegisteredTool(tool, name)
			if class.Effect != permission.EffectUnknown || permission.Decide(permission.ModeAutoApproval, class) != permission.DecisionRequireApproval {
				t.Fatalf("unregistered or MCP tool acquired builtin permissions: name=%s class=%+v", name, class)
			}
		}
	}
}

func TestBuiltinRegistrationsHaveExplicitPermissionIdentities(t *testing.T) {
	set, err := tools.New(t.TempDir(), "permission-test", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	for _, tool := range set.AllTools() {
		name := tool.Info().Name
		class := classifyRegisteredTool(tool, name)
		want := permission.EffectReadOnly
		switch name {
		case "bash":
			want = permission.EffectUnknown
		case "edit", "write":
			want = permission.EffectMutating
		}
		if class.Identity != "builtin:"+name || class.Effect != want {
			t.Fatalf("registration %s: %+v, want %s", name, class, want)
		}
	}
}

func TestAutoApprovalBashInheritedEnvironmentCannotExecuteBeforeApproval(t *testing.T) {
	for _, environment := range []string{"exported function", "BASH_ENV"} {
		t.Run(environment, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "side-effect")
			t.Setenv("KAGUYA_PERMISSION_MARKER", marker)
			if environment == "exported function" {
				t.Setenv("BASH_FUNC_pwd%%", `() { touch "$KAGUYA_PERMISSION_MARKER"; }`)
			} else {
				profile := filepath.Join(dir, "profile.sh")
				if err := os.WriteFile(profile, []byte("touch \"$KAGUYA_PERMISSION_MARKER\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BASH_ENV", profile)
			}
			set, err := tools.New(dir, "permission-test", zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			defer set.Close()
			approver := &rejectingApprover{}
			wrapped := permission.WrapAll(permission.ModeAutoApproval, approver, classifyRegisteredTools([]fantasy.AgentTool{set.BashTool()}))
			response, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{ID: "bash-call", Name: "bash", Input: `{"command":"pwd"}`})
			if err != nil || !response.IsError || approver.calls != 1 {
				t.Fatalf("bash bypassed approval: response=%+v err=%v approvals=%d", response, err, approver.calls)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("inherited environment executed before approval: %v", err)
			}
		})
	}
}
