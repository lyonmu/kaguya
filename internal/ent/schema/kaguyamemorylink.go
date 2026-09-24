package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemoryLink 保存页面之间的关系；替代关系（supersedes）由编译或用户显式建立。
type KaguyaMemoryLink struct{ ent.Schema }

func (KaguyaMemoryLink) Fields() []ent.Field {
	return []ent.Field{
		field.String("from_page_id").MaxLen(64).NotEmpty(),
		field.String("to_page_id").MaxLen(64).NotEmpty(),
		field.Enum("relation").Values("related", "supersedes").Default("related"),
	}
}

func (KaguyaMemoryLink) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryLink) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("from_page_id", "to_page_id", "relation").Unique(),
		index.Fields("to_page_id"),
	}
}

func (KaguyaMemoryLink) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_link", "记忆页面关系")
}
