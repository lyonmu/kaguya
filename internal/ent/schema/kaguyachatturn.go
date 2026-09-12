package schema

import (
	"charm.land/fantasy"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaChatTurn 一次问答；状态区分进行中、已提交与中断/失败的轮次。
type KaguyaChatTurn struct{ ent.Schema }

func (KaguyaChatTurn) Fields() []ent.Field {
	return []ent.Field{
		field.String("conversation_id").MaxLen(64).NotEmpty(),
		field.Int64("turn_index").Positive(),
		field.Text("user_content"),
		field.String("provider_id"), field.String("provider_name"),
		field.String("model_id"), field.String("model_name"), field.String("api_protocol"),
		field.Enum("status").Values("running", "completed", "interrupted", "canceled", "failed").Default("completed").
			Comment("running 生成中；completed 完整提交；interrupted 断联/超时；canceled 用户主动停止；failed 生成失败；旧记录默认 completed"),
		field.Time("started_at"), field.Time("finished_at"),
		field.Int64("duration_ms").NonNegative(), field.Int64("tool_calls").NonNegative(),
		field.String("finish_reason"),
		field.Int64("input_tokens").NonNegative(), field.Int64("output_tokens").NonNegative(),
		field.Int64("total_tokens").NonNegative(), field.Int64("cached_tokens").NonNegative(),
		field.Int64("reasoning_tokens").NonNegative(),
		field.Int64("context_tokens").Optional().Nillable().NonNegative().Comment("最后一次模型调用输入（含缓存）及输出，用于估算整段上下文；旧记录未知"),
		field.Int("context_window").Default(0).NonNegative().Comment("本轮模型 token_context_window 快照，0 表示未知"),
		// 不使用展示块重建上下文：工具消息、推理签名和 provider metadata 必须无损保留。
		field.JSON("context_messages", []fantasy.Message{}).Optional().Comment("发生压缩后的完整续聊快照；原始 messages 始终保留"),
		field.Int("compaction_count").Default(0).NonNegative(),
		field.JSON("messages", []fantasy.Message{}).Comment("仅本轮用户/模型/工具上下文，不含历史前缀；不直接返回前端"),
	}
}
func (KaguyaChatTurn) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("conversation", KaguyaConversation.Type).Ref("turns").Field("conversation_id").Unique().Required(),
		edge.To("blocks", KaguyaChatBlock.Type),
	}
}
func (KaguyaChatTurn) Mixin() []ent.Mixin { return chatHistoryMixins() }
func (KaguyaChatTurn) Indexes() []ent.Index {
	return []ent.Index{index.Fields("conversation_id", "turn_index").Unique(), index.Fields("finished_at")}
}
func (KaguyaChatTurn) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_chat_turn", "已完整提交的问答及累计用量")
}
