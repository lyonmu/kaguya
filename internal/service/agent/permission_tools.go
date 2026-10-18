package agent

import (
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

// toolIdentityProvider 由本机工厂与 MCP 适配器提供注册身份；名称不授予权限。
// 接口定义在消费侧，避免工具层依赖 service。
type toolIdentityProvider interface{ PermissionIdentity() string }

func classifyRegisteredTools(tools []fantasy.AgentTool) []permission.GatedTool {
	gated := make([]permission.GatedTool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			gated = append(gated, permission.GatedTool{Tool: tool, Class: classifyRegisteredTool(tool, tool.Info().Name)})
		}
	}
	return gated
}

// classifyRegisteredTool 先检查注册身份。远端 hint 和 Info 名称不能冒充本机工具。
func classifyRegisteredTool(tool fantasy.AgentTool, name string) permission.Classification {
	identity := "unknown:" + name
	if provider, ok := tool.(toolIdentityProvider); ok {
		if registered := provider.PermissionIdentity(); registered != "" {
			identity = registered
		}
	}
	class := permission.Classification{Identity: identity, Effect: permission.EffectUnknown}
	switch identity {
	case "builtin:read", "builtin:grep", "builtin:find", "builtin:ls", "memory:memory_search", "memory:memory_read":
		class.Effect = permission.EffectReadOnly
	case "builtin:edit", "builtin:write":
		class.Effect = permission.EffectMutating
	}
	return class
}

// turnReferenceSummary 把可空的引用摘要转换为 Ent JSON 值；nil 时写入空结构，
// 旧记录与无引用轮次保持空数组语义。
func turnReferenceSummary(summary *dtochat.TurnReferenceSummary) dtochat.TurnReferenceSummary {
	if summary == nil {
		return dtochat.TurnReferenceSummary{}
	}
	return *summary
}
