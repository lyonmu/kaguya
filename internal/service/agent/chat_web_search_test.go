package agent

import (
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/agent/permission"
	codingtools "github.com/lyonmu/kaguya/internal/agent/tools"
	"github.com/lyonmu/kaguya/internal/agent/webtools"
	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

func TestChatRegistersBothWebToolsForToolCapableModels(t *testing.T) {
	ctx, _ := setupChatTest(t)
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)

	workspace, err := codingtools.New(t.TempDir(), "test-web-search", global.Logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	svc := &servicesystem.SystemSvc{}
	info, err := svc.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := &chatTarget{info: info, model: &ent.KaguyaModelsInfo{ModelID: "not-a-deepseek-model", APIProtocol: consts.ProtocolAnthropic}}
	for _, tc := range []struct {
		name    string
		enabled bool
		capable bool
		project bool
	}{
		{"disabled", false, true, false},
		{"ordinary chat", true, true, false},
		{"project chat", true, true, true},
		{"unsupported model", true, false, false},
		{"disabled again", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateWebSearchConfig(ctx, &dtosystem.WebSearchSaveReq{Enabled: &tc.enabled, APIKey: "test-web-search-key"})
			if err != nil {
				t.Fatal(err)
			}
			target.model.CapabilityToolUse = consts.IsFalse
			if tc.capable {
				target.model.CapabilityToolUse = consts.IsTrue
			}
			var set *codingtools.Set
			var projectID string
			if tc.project {
				set, projectID = workspace, "project-id"
			}
			prompt, err := prepareChatPrompt(ctx, target, set, "test-web-search", projectID, nil, &dtochat.ChatReq{Messages: "Read the web."}, memoryTurn{})
			if err != nil {
				t.Fatal(err)
			}
			registered := make(map[string]int)
			for _, tool := range prompt.tools {
				name := tool.Info().Name
				registered[name]++
				if name != webtools.SearchName && name != webtools.FetchName {
					continue
				}
				class := classifyRegisteredTool(tool, name)
				if class.Identity != "builtin:"+name ||
					permission.Decide(permission.ModeAutoApproval, class) != permission.DecisionRequireApproval ||
					permission.Decide(permission.ModeFullApproval, class) != permission.DecisionRequireApproval ||
					permission.Decide(permission.ModeFullAccess, class) != permission.DecisionAllow {
					t.Fatalf("unexpected web tool permission: %+v", class)
				}
			}
			want := 0
			if tc.enabled && tc.capable {
				want = 1
			}
			if registered[webtools.SearchName] != want || registered[webtools.FetchName] != want {
				t.Fatalf("registered tools %v, expected %d of each web tool", registered, want)
			}
			if tc.project && registered["read"] != 1 {
				t.Fatal("project tool was lost")
			}
		})
	}
}
