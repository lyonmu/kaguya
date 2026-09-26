package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemoryJob 是可恢复的有限后台作业与待审提案容器：
// 固定输入来源集合、租约、重试与有界 result_json 都在行内持久化。
type KaguyaMemoryJob struct{ ent.Schema }

func (KaguyaMemoryJob) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("kind").Values("compile", "backfill").Default("compile"),
		field.String("scope_key").MaxLen(128).NotEmpty(),
		field.String("conversation_id").MaxLen(64).Default(""),
		field.JSON("input_source_ids", []string{}).Optional().Comment("冻结的输入来源 ID 集合，领取后不再变化"),
		field.String("input_hash").MaxLen(64).Default(""),
		field.Int("source_budget").Default(0).NonNegative().Comment("领取时冻结的来源字节预算，保证 input_hash 可确定性重建"),
		field.String("compiler_version").MaxLen(32).Default(""),
		field.Enum("status").Values(
			"pending", "running", "succeeded", "retry_wait", "blocked", "needs_review", "failed", "canceled",
		).Default("pending"),
		field.Int("attempt").Default(0).NonNegative().Comment("已执行尝试次数"),
		field.String("lease_token").MaxLen(64).Default("").Comment("每次尝试使用新 token，避免只看过期时间产生 ABA 问题"),
		field.Time("lease_expires_at").Optional().Nillable(),
		field.Time("next_attempt_at").Optional().Nillable().Comment("retry_wait 的下次执行时间"),
		field.String("error_code").MaxLen(64).Default(""),
		field.Text("error_summary").Default("").Comment("不含源文本或上游完整请求的错误摘要"),
		field.Text("result_json").Default("").Comment("有界结果：noop 说明或待审 PatchPlan"),
		field.Int64("policy_epoch").Default(0).NonNegative().Comment("领取时的记忆策略版本"),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
	}
}

func (KaguyaMemoryJob) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "next_attempt_at"),
		index.Fields("conversation_id", "scope_key"),
		index.Fields("lease_expires_at"),
	}
}

func (KaguyaMemoryJob) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_job", "后台记忆编译作业与待审提案")
}
