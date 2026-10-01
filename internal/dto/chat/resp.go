package chat

import "github.com/lyonmu/kaguya/internal/consts"

type ChatFlag string

const (
	ChatFlagStart    ChatFlag = "start"    // 下行：本轮首帧（会话 ID + 模型信息）
	ChatFlagDelta    ChatFlag = "delta"    // 下行：增量内容帧
	ChatFlagDone     ChatFlag = "done"     // 下行：唯一的本轮结束帧，仅携带 Usage，不重复内容
	ChatFlagError    ChatFlag = "error"    // 下行：出错或被取消
	ChatFlagApproval ChatFlag = "approval" // 下行：权限审批状态帧，不结束本轮
)

type ChatResp struct {
	Seq               int64                   `json:"seq,omitempty"`                // 轮内发帧序号，由 TurnEmitter 串行分配
	FinishReason      string                  `json:"finish_reason,omitempty"`      // stop 或 step_limit（已保存，可继续）
	MemoryUnavailable bool                    `json:"memory_unavailable,omitempty"` // 长期记忆检索暂不可用的非致命状态，不改变无记忆语义
	Chat              Chat                    `json:"chat"`                         // 对话信息
	Approval          *Approval               `json:"approval,omitempty"`           // 审批状态；仅 flag=approval 帧携带
	APIProtocol       consts.ProviderProtocol `json:"api_protocol"`                 // api接口类型
	Usage             Usage                   `json:"usage"`                        // token 使用信息
	Created           int64                   `json:"created"`                      // 创建时间时间戳
	ModelID           string                  `json:"model_id"`                     // 模型id
	ModelName         string                  `json:"model_name"`                   // 模型名称
	Err               error                   `json:"-"`                            // 仅服务端/API mapper 使用，不向客户端序列化
}

// Approval 是一次工具审批的完整状态；input 只在当前连接展示，不写入审计表。
type Approval struct {
	ApprovalID   string `json:"approval_id"`
	ToolCallID   string `json:"tool_call_id"`
	ToolName     string `json:"tool_name"`
	ToolIdentity string `json:"tool_identity,omitempty"`
	Effect       string `json:"effect"`
	Mode         string `json:"mode"`
	Status       string `json:"status"` // pending/approved/rejected/expired/canceled/consumed
	Input        string `json:"input,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

// ApprovalDecisionResp 是裁决接口的幂等结果；重复相同裁决返回同一状态。
type ApprovalDecisionResp struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
	Decision   string `json:"decision,omitempty"`
}

// ApprovalDecisionReq 是审批裁决请求体；decision 只接受 approve/reject。
type ApprovalDecisionReq struct {
	Decision string `json:"decision" binding:"required,oneof=approve reject"`
}

// ApprovalIDReq 定位会话内的一次审批。
type ApprovalIDReq struct {
	ID         string `uri:"id" binding:"required,max=64"`
	ApprovalID string `uri:"approval_id" binding:"required,max=64"`
}

// BlockType 标识内容用途，与整轮生命周期 Flag 独立。
type BlockType string

const (
	BlockTypeText       BlockType = "text"
	BlockTypeReasoning  BlockType = "reasoning"
	BlockTypeToolCall   BlockType = "tool_call"
	BlockTypeToolResult BlockType = "tool_result"
)

type BlockPhase string

const (
	BlockPhaseStart BlockPhase = "start"
	BlockPhaseDelta BlockPhase = "delta"
	BlockPhaseEnd   BlockPhase = "block_end"
)

// ContentBlock 的 delta 帧中 Text/Input 是增量；正文/思考的 block_end 只标记结束。
// 工具调用的 block_end 携带最终 Input（替换增量参数），工具结果一次性发送。
// 工具输入保留字符串，因为流式 JSON 片段及无效工具参数不一定是合法 JSON。
type ContentBlock struct {
	Type             BlockType   `json:"type"`
	Phase            BlockPhase  `json:"phase"`
	Text             string      `json:"text,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	ToolName         string      `json:"tool_name,omitempty"`
	Input            string      `json:"input,omitempty"`
	Output           *ToolOutput `json:"output,omitempty"`
	ProviderExecuted bool        `json:"provider_executed,omitempty"`
	IsError          bool        `json:"is_error,omitempty"`
	ErrorMessage     string      `json:"error_message,omitempty"`
}

type ToolOutputType string

const (
	ToolOutputText  ToolOutputType = "text"
	ToolOutputError ToolOutputType = "error"
	ToolOutputMedia ToolOutputType = "media"
)

type ToolOutput struct {
	Type      ToolOutputType `json:"type"`
	Text      string         `json:"text,omitempty"`
	Data      string         `json:"data,omitempty"` // base64 媒体数据
	MediaType string         `json:"media_type,omitempty"`
	Code      int            `json:"code,omitempty"` // 结构化错误码；审批拒绝等由工具元数据映射，旧记录为 0
}

type Chat struct {
	ID      string        `json:"id"`                // 会话雪花 ID；后续请求沿用此 ID 恢复上下文
	Content string        `json:"content,omitempty"` // 兼容字段：仅正文增量，与 block.text 二选一消费
	Flag    ChatFlag      `json:"flag"`
	Block   *ContentBlock `json:"block,omitempty"` // delta 帧的内容块事件；整轮 done 不携带内容
}

type Usage struct {
	InputTokens         int `json:"input_tokens"`          // 输入 Token 数（不含缓存读写）
	OutputTokens        int `json:"output_tokens"`         // 输出 Token 数
	TotalTokens         int `json:"total_tokens"`          // 总 Token 数
	CachedTokens        int `json:"cached_tokens"`         // 命中缓存的 Token 数（缓存读取）
	CacheCreationTokens int `json:"cache_creation_tokens"` // 缓存写入的 Token 数
	ReasoningTokens     int `json:"reasoning_tokens"`      // 思考 Token 数（提供商未报告时为 0）
}
