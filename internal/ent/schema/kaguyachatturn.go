package schema

import (
	"charm.land/fantasy"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaChatTurn 一次完整成功的问答；失败/取消轮次不写入。
type KaguyaChatTurn struct{ ent.Schema }

func (KaguyaChatTurn) Fields() []ent.Field {
	return []ent.Field{
		field.String("conversation_id").MaxLen(64).NotEmpty(),
		field.Int64("turn_index").Positive(),
		field.Text("user_content").SchemaType(map[string]string{dialect.MySQL: "longtext"}),
		field.String("provider_id"), field.String("provider_name"),
		field.String("model_id"), field.String("model_name"), field.String("api_protocol"),
		field.Time("started_at"), field.Time("finished_at"),
		field.Int64("duration_ms").NonNegative(), field.Int64("tool_calls").NonNegative(),
		field.String("finish_reason"),
		field.Int64("input_tokens").NonNegative(), field.Int64("output_tokens").NonNegative(),
		field.Int64("total_tokens").NonNegative(), field.Int64("cached_tokens").NonNegative(),
		field.Int64("reasoning_tokens").NonNegative(),
		// 不使用展示块重建上下文：工具消息、推理签名和 provider metadata 必须无损保留。
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
	return []ent.Index{index.Fields("conversation_id", "turn_index").Unique()}
}
func (KaguyaChatTurn) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_chat_turn", "已完整提交的问答及累计用量")
}
