package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemorySearchDoc 是 FTS5 的搜索投影：显式 INTEGER PRIMARY KEY 作为稳定
// rowid，页面 ID 不直接当 FTS rowid。投影文本由 normalizer 版本化的 Go 代码生成，
// FTS 虚拟表与 triggers 由 internal/db 的版本化迁移管理。
type KaguyaMemorySearchDoc struct{ ent.Schema }

func (KaguyaMemorySearchDoc) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").Positive().Comment("显式 INTEGER PRIMARY KEY，跨重建与 VACUUM 稳定"),
		field.String("page_id").MaxLen(64).NotEmpty().Unique(),
		field.Int64("page_version").Positive(),
		field.Int("normalizer_version").Positive(),
		field.Text("title_terms").Default(""),
		field.Text("alias_terms").Default(""),
		field.Text("summary_terms").Default(""),
		field.Text("body_terms").Default(""),
	}
}

func (KaguyaMemorySearchDoc) Indexes() []ent.Index {
	return []ent.Index{index.Fields("page_version")}
}

func (KaguyaMemorySearchDoc) Annotations() []schema.Annotation {
	enabled := true
	return []schema.Annotation{schema.Comment("长期记忆检索投影"), entsql.Annotation{Table: "kaguya_memory_search_doc", WithComments: &enabled}}
}
