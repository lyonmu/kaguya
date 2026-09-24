package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemoryPage 是可溯源的长期记忆页面；正文以 Markdown 保存，
// 索引、关系与版本由代码维护，不以磁盘 Markdown 文件作为主存储。
type KaguyaMemoryPage struct{ ent.Schema }

func (KaguyaMemoryPage) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope_key").MaxLen(128).NotEmpty(),
		field.String("canonical_key").MaxLen(160).NotEmpty().Comment("同范围内稳定主题键，重命名不改变 ID；辅助去重"),
		field.Enum("kind").Values(
			"preference", "fact", "decision", "procedure", "lesson",
		),
		field.String("title").NotEmpty(),
		field.Text("summary").Default(""),
		field.Text("body").Sensitive(),
		field.JSON("aliases", []string{}).Optional(),
		field.Enum("status").Values(
			"proposed", "active", "conflicted", "stale", "archived", "deleted",
		).Default("proposed").Comment("deleted 保留最小 tombstone，防止自动复活"),
		field.Int64("version").Default(1).Positive().Comment("乐观并发版本，发布与编辑都按版本条件更新"),
		field.Bool("pinned").Default(false).Comment("影响预算内召回"),
		field.Bool("user_locked").Default(false).Comment("阻止自动覆盖"),
		field.Time("expires_at").Optional().Nillable().Comment("过期后默认不自动注入，显式搜索可返回并标记"),
	}
}

func (KaguyaMemoryPage) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryPage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope_key", "canonical_key").Unique(),
		index.Fields("scope_key", "status", "updated_at"),
	}
}

func (KaguyaMemoryPage) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_page", "可溯源的长期记忆页面")
}
