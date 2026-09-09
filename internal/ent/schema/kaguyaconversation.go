package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// KaguyaConversation 只包含至少一轮已完整提交的会话。
type KaguyaConversation struct{ ent.Schema }

func (KaguyaConversation) Fields() []ent.Field {
	return []ent.Field{
		field.String("title").MaxLen(200).NotEmpty(),
		field.String("project_id").Optional().Nillable(),
		field.Bool("favorite").Default(false),
		field.Int64("turn_count").Default(0).NonNegative().Comment("已提交轮数，同时用于乐观并发校验"),
		field.Time("last_message_at"),
		field.String("model_id"),
		field.String("model_name"),
		field.Int64("duration_ms").Default(0).NonNegative(),
		field.Int64("tool_calls").Default(0).NonNegative(),
		field.Int64("input_tokens").Default(0).NonNegative(),
		field.Int64("output_tokens").Default(0).NonNegative(),
		field.Int64("total_tokens").Default(0).NonNegative(),
		field.Int64("cached_tokens").Default(0).NonNegative(),
		field.Int64("reasoning_tokens").Default(0).NonNegative(),
	}
}
func (KaguyaConversation) Edges() []ent.Edge {
	return []ent.Edge{edge.To("turns", KaguyaChatTurn.Type), edge.From("project", KaguyaProject.Type).Ref("conversations").Field("project_id").Unique()}
}
func (KaguyaConversation) Mixin() []ent.Mixin { return chatHistoryMixins() }
func (KaguyaConversation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("deleted_at", "last_message_at", "id"),
		index.Fields("deleted_at", "favorite", "last_message_at", "id"),
		index.Fields("deleted_at", "title"),
		index.Fields("project_id", "deleted_at", "last_message_at", "id"),
	}
}
func (KaguyaConversation) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_conversation", "已完成对话的会话摘要")
}

func chatHistoryMixins() []ent.Mixin {
	return []ent.Mixin{pkg.NewIDMixin(func() string {
		id, err := global.Id.GenID()
		if err != nil {
			panic(fmt.Sprintf("failed to generate ID: %v", err))
		}
		return fmt.Sprintf("%d", id)
	}), pkg.TimeMixin{}}
}
func chatHistoryAnnotations(table, comment string) []schema.Annotation {
	enabled := true
	return []schema.Annotation{schema.Comment(comment), entsql.Annotation{Table: table, Charset: "utf8mb4", Collation: "utf8mb4_general_ci", WithComments: &enabled}, edge.Annotation{StructTag: `json:"-" gorm:"-"`}}
}
