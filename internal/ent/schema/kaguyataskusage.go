package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaTaskUsage 保存后台模型调用用量，不随会话或模型删除而丢失。
type KaguyaTaskUsage struct{ ent.Schema }

func (KaguyaTaskUsage) Fields() []ent.Field {
	return []ent.Field{
		field.String("kind").NotEmpty(),
		field.String("conversation_id").Default(""),
		field.String("provider_id").Default(""), field.String("provider_name").Default(""),
		field.String("model_id").Default(""), field.String("model_name").Default(""),
		field.Time("finished_at"), field.Bool("usage_known").Default(false),
		field.Int64("input_tokens").Default(0).NonNegative(), field.Int64("output_tokens").Default(0).NonNegative(),
		field.Int64("reasoning_tokens").Default(0).NonNegative(), field.Int64("cached_tokens").Default(0).NonNegative(),
		field.Int64("total_tokens").Default(0).NonNegative(),
	}
}
func (KaguyaTaskUsage) Mixin() []ent.Mixin   { return chatHistoryMixins() }
func (KaguyaTaskUsage) Indexes() []ent.Index { return []ent.Index{index.Fields("finished_at")} }
func (KaguyaTaskUsage) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_task_usage", "后台模型调用用量")
}
