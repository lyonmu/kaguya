package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaProject associates host directories with conversations; never owns host files.
type KaguyaProject struct{ ent.Schema }

func (KaguyaProject) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(200),
		field.String("path").NotEmpty().MaxLen(4096),
		field.String("description").Default("").MaxLen(2000),
	}
}
func (KaguyaProject) Edges() []ent.Edge {
	return []ent.Edge{edge.To("conversations", KaguyaConversation.Type)}
}
func (KaguyaProject) Mixin() []ent.Mixin { return chatHistoryMixins() }
func (KaguyaProject) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("deleted_at", "name", "id"),
		index.Fields("path").Unique().Annotations(entsql.IndexWhere("deleted_at IS NULL")),
	}
}
func (KaguyaProject) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_project", "主机目录项目")
}
