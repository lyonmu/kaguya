package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemoryAttempt 记录每一次任务模型调用的用量与结果码；
// 失败重试与修复调用同样可能收费，不能只记最终成功用量。
type KaguyaMemoryAttempt struct{ ent.Schema }

func (KaguyaMemoryAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.String("job_id").MaxLen(64).NotEmpty(),
		field.Int("attempt").Positive().Comment("作业执行尝试序号"),
		field.Enum("phase").Values("extract", "plan", "repair").Default("extract"),
		field.String("model_record_id").MaxLen(64).Default("").Comment("本地模型记录 ID 快照"),
		field.String("provider_id").MaxLen(64).Default(""),
		field.String("upstream_model_id").MaxLen(200).Default("").Comment("调用时使用的上游模型快照"),
		field.Bool("usage_known").Default(false).Comment("错误路径未返回用量时为 false，不能按 0 计入确认消耗"),
		field.Int64("input_tokens").Default(0).NonNegative(),
		field.Int64("output_tokens").Default(0).NonNegative(),
		field.Int64("total_tokens").Default(0).NonNegative(),
		field.Int64("cached_tokens").Default(0).NonNegative(),
		field.Int64("reasoning_tokens").Default(0).NonNegative(),
		field.Int64("duration_ms").Default(0).NonNegative(),
		field.String("result_code").MaxLen(64).Default("").Comment("ok / invalid_json / provider_error / budget / blocked 等安全错误码"),
	}
}

func (KaguyaMemoryAttempt) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryAttempt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id", "attempt"),
	}
}

func (KaguyaMemoryAttempt) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_attempt", "任务模型调用尝试与用量")
}
