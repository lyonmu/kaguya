package agent

import (
	"encoding/json"
	"errors"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
)

// ErrPermissionModeInvalid 表示权限模式非法，或续聊请求携带了与数据库不一致
// 的模式；续聊模式必须先 PUT 修改会话记录。
var ErrPermissionModeInvalid = errors.New("invalid permission mode")

// resolvePermissionMode 解析本轮权限模式：新会话使用请求值，缺省 full_access；
// 续聊以数据库为权威，请求值相同可用，不同则拒绝；未知持久化值不静默放宽。
func resolvePermissionMode(row *ent.KaguyaConversation, requested string) (permission.Mode, error) {
	if row == nil {
		if requested == "" {
			return permission.ModeFullAccess, nil
		}
		mode, ok := permission.Parse(requested)
		if !ok {
			return "", ErrPermissionModeInvalid
		}
		return mode, nil
	}
	stored, ok := permission.Parse(string(row.PermissionMode))
	if !ok {
		return "", ErrPermissionModeInvalid
	}
	if requested != "" {
		requestedMode, ok := permission.Parse(requested)
		if !ok || requestedMode != stored {
			return "", ErrPermissionModeInvalid
		}
	}
	return stored, nil
}

// defaultPermissionMode 兼容直接写库的旧路径与测试：空模式按兼容默认 full_access，
// 不改变旧记录“未审批过”的语义。
func defaultPermissionMode(mode string) string {
	if mode == "" {
		return string(permission.ModeFullAccess)
	}
	return mode
}

// toolIdentityProvider 由 MCP 适配器实现；接口定义在消费侧，保持 agent/mcp
// 不依赖 service。身份绑定 server/remote/generation/schema，不按模型名字猜测。
type toolIdentityProvider interface{ PermissionIdentity() string }

// classifyRegisteredTools 为全部已注册工具建立可信 descriptor。内置工具来自
// 本进程注册表；MCP 一律 unknown；无法识别的一律 unknown，绝不放行。
func classifyRegisteredTools(tools []fantasy.AgentTool) []permission.GatedTool {
	gated := make([]permission.GatedTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := tool.Info().Name
		class := classifyRegisteredTool(tool, name)
		item := permission.GatedTool{Tool: tool, Class: class}
		if name == "bash" {
			item.Classify = classifyBashCall
		}
		gated = append(gated, item)
	}
	return gated
}

// classifyRegisteredTool 返回单个工具的可信身份与副作用分类。
func classifyRegisteredTool(tool fantasy.AgentTool, name string) permission.Classification {
	switch name {
	case "read", "grep", "find", "ls":
		return permission.Classification{Identity: "builtin:" + name, Effect: permission.EffectReadOnly}
	case "edit", "write":
		return permission.Classification{Identity: "builtin:" + name, Effect: permission.EffectMutating}
	case "bash":
		return permission.Classification{Identity: "builtin:bash", Effect: permission.EffectUnknown}
	case "memory_search", "memory_read":
		return permission.Classification{Identity: "memory:" + name, Effect: permission.EffectReadOnly}
	}
	if provider, ok := tool.(toolIdentityProvider); ok {
		if identity := provider.PermissionIdentity(); identity != "" {
			// MCP 远端 hint 不是本机信任来源；默认 unknown，必须审批。
			return permission.Classification{Identity: identity, Effect: permission.EffectUnknown}
		}
	}
	return permission.Classification{Identity: "unknown:" + name, Effect: permission.EffectUnknown}
}

// classifyBashCall 用固定版本的 AST 解析器对最终命令动态分类；解析失败或
// 不在只读白名单时返回 unknown，保留静态 descriptor 的身份。
func classifyBashCall(call fantasy.ToolCall) permission.Classification {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return permission.Classification{Identity: "builtin:bash", Effect: permission.EffectUnknown}
	}
	return permission.ShellClassification(input.Command)
}

// turnReferenceSummary 把可空的引用摘要转换为 Ent JSON 值；nil 时写入空结构，
// 旧记录与无引用轮次保持空数组语义。
func turnReferenceSummary(summary *dtochat.TurnReferenceSummary) dtochat.TurnReferenceSummary {
	if summary == nil {
		return dtochat.TurnReferenceSummary{}
	}
	return *summary
}
