package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemoryEvidence 把修订中的主张定位到来源片段，而不只是“来自某次聊天”；
// quote 必须是对应 part 的子串，发布前做确定性校验。
type KaguyaMemoryEvidence struct{ ent.Schema }

func (KaguyaMemoryEvidence) Fields() []ent.Field {
	return []ent.Field{
		field.String("revision_id").MaxLen(64).NotEmpty(),
		field.String("claim_key").MaxLen(200).NotEmpty(),
		field.String("source_id").MaxLen(64).NotEmpty(),
		field.String("part_key").MaxLen(200).NotEmpty(),
		field.Text("quote"),
		field.String("quote_hash").MaxLen(64).Default(""),
		field.Enum("relation").Values("support", "refute").Default("support"),
		field.Enum("basis").Values(
			"user_statement", "tool_observation", "document_statement", "synthesis",
		).Default("synthesis").Comment("证据基础：用户陈述 / 工具观察 / 资料陈述 / 综合推断"),
	}
}

func (KaguyaMemoryEvidence) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryEvidence) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("revision_id", "claim_key"),
		index.Fields("source_id"),
	}
}

func (KaguyaMemoryEvidence) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_evidence", "主张到来源片段的证据引用")
}
