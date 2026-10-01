package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaApproval 是工具审批审计表：只保存关联 ID、工具标识、输入哈希与状态，
// 不保存完整命令或参数。pending 必须持久成功后才允许裁决；崩溃遗留不重放执行。
type KaguyaApproval struct{ ent.Schema }

func (KaguyaApproval) Fields() []ent.Field {
	return []ent.Field{
		field.String("approval_id").MaxLen(64).NotEmpty().Comment("随机高熵审批 ID，绑定单次工具调用"),
		field.String("conversation_id").MaxLen(64).NotEmpty(),
		field.String("turn_id").MaxLen(64).NotEmpty(),
		field.String("tool_call_id").MaxLen(128).NotEmpty(),
		field.String("input_hash").MaxLen(64).NotEmpty().Comment("最终完整参数哈希；裁决接口不能替换参数"),
		field.String("tool_identity").MaxLen(256).NotEmpty().Comment("可信工具身份：内置名或 MCP server/remote/generation/schema 绑定"),
		field.Enum("effect").Values("read_only", "mutating", "unknown").Default("unknown"),
		field.Enum("mode").Values("full_approval", "auto_approval", "full_access").Default("full_access"),
		field.Enum("status").Values("pending", "approved", "rejected", "expired", "canceled", "consumed").Default("pending"),
		field.String("decision").Default("").Comment("approve 或 reject；pending 为空"),
		field.Time("requested_at"),
		field.Time("resolved_at").Optional().Nillable(),
		field.Time("consumed_at").Optional().Nillable(),
		field.String("reason_code").Default("").Comment("安全原因码，不保存原始错误或参数"),
	}
}

func (KaguyaApproval) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaApproval) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("approval_id").Unique(),
		index.Fields("turn_id", "status"),
		index.Fields("turn_id", "tool_call_id").Unique(),
	}
}

func (KaguyaApproval) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_approval", "工具审批审计")
}
