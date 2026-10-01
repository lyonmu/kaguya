package permission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"charm.land/fantasy"
)

// Decision 是策略对单次工具调用的裁决结果。
type Decision string

const (
	// DecisionAllow 直接执行原工具。
	DecisionAllow Decision = "allow"
	// DecisionRequireApproval 必须先取得用户批准才允许执行。
	DecisionRequireApproval Decision = "require_approval"
)

// Classification 是注册工具的可信身份与副作用分类。它来自注册点维护的
// descriptor，绝不按模型提供的工具名猜测；MCP 等未知来源默认 unknown。
type Classification struct {
	// Identity 是可信工具身份：内置工具名或 MCP 的 server/remote/generation/
	// schema 绑定，不参与模型调用参数。
	Identity string
	// Effect 是副作用分类；unknown 始终按最保守策略处理。
	Effect Effect
	// ReadOnlyProven 只对已验证的只读执行 profile 为 true（例如 shell AST
	// 证明的只读命令）。它只能在 auto_approval 下放行，不能覆盖 mutating。
	ReadOnlyProven bool
}

// Decide 按会话模式与可信分类计算裁决。未知模式不 fallback 到 full_access，
// 一律要求审批；mode 本身在进入执行链前已由服务层校验。
func Decide(mode Mode, class Classification) Decision {
	switch mode {
	case ModeFullAccess:
		return DecisionAllow
	case ModeAutoApproval:
		if class.Effect == EffectReadOnly || class.ReadOnlyProven {
			return DecisionAllow
		}
	}
	return DecisionRequireApproval
}

// Request 是一次待审批工具调用的完整身份。Input 是最终完整参数，InputHash
// 是其稳定摘要；审批登记后裁决接口不能替换两者，否则拒绝执行。
type Request struct {
	ApprovalID     string
	ConversationID string
	TurnID         string
	ToolCallID     string
	ToolIdentity   string
	ToolName       string
	Effect         Effect
	Mode           Mode
	Input          string
	InputHash      string
}

// Outcome 是审批等待的结果。
type Outcome string

const (
	OutcomeApproved Outcome = "approved"
	OutcomeRejected Outcome = "rejected"
	OutcomeExpired  Outcome = "expired"
	OutcomeCanceled Outcome = "canceled"
)

// Approver 等待并返回一次用户裁决。实现必须在调用返回的 Request 上补齐
// ApprovalID/ConversationID/TurnID（按值传入时可通过返回值或指针回填）。
// 实现必须尊重 ctx 取消，并在所有出口释放等待资源、注销待裁决记录。
type Approver interface {
	Await(ctx context.Context, req *Request) (Outcome, error)
}

// HashInput 返回最终工具参数的稳定 sha256 摘要。
func HashInput(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// RestrictedModeAllowsProviderTools 报告模式是否允许 provider-executed 调用；
// 只有 full_access 沿用历史行为，受限模式必须拒绝。
func RestrictedModeAllowsProviderTools(mode Mode) bool { return mode == ModeFullAccess }

// Metadata 是审批裁决写入工具结果的元数据，供历史/SSE 映射为结构化 code。
type Metadata struct {
	ApprovalID string `json:"approval_id,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

// WrapMetadata 把裁决元数据写入 fantasy.ToolResponse.Metadata。
func WrapMetadata(response fantasy.ToolResponse, metadata Metadata) fantasy.ToolResponse {
	return fantasy.WithResponseMetadata(response, metadata)
}
