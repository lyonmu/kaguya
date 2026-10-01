// Package permission 定义会话工具权限模式与工具效应分类的稳定枚举。
// 它只依赖 context 与 fantasy，不 import Gin/Ent/service，供 DB enum、DTO 与
// 前端标签共享同一组值。
package permission

// Mode 是会话级工具权限模式；值是持久化契约，不能随意改名。
type Mode string

const (
	// ModeFullApproval（完全审批）要求所有工具调用逐个批准。
	ModeFullApproval Mode = "full_approval"
	// ModeAutoApproval（自主审批）放行可信只读工具，其余仍需批准。
	ModeAutoApproval Mode = "auto_approval"
	// ModeFullAccess（完全访问）放行全部工具，是兼容默认值。
	ModeFullAccess Mode = "full_access"
)

// Modes 按稳定顺序返回全部合法模式，供 Ent enum 与校验复用。
func Modes() []string {
	return []string{string(ModeFullApproval), string(ModeAutoApproval), string(ModeFullAccess)}
}

// Valid 报告模式是否为已知值；未知值必须拒绝，不能 fallback 到 full_access。
func (m Mode) Valid() bool {
	switch m {
	case ModeFullApproval, ModeAutoApproval, ModeFullAccess:
		return true
	default:
		return false
	}
}

// Parse 解析持久化/请求中的模式文本，未知值返回 false。
func Parse(value string) (Mode, bool) {
	mode := Mode(value)
	return mode, mode.Valid()
}

// Effect 是工具调用的副作用分类；unknown 必须按最保守策略处理。
type Effect string

const (
	EffectReadOnly Effect = "read_only"
	EffectMutating Effect = "mutating"
	EffectUnknown  Effect = "unknown"
)
