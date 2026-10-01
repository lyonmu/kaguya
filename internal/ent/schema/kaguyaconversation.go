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
		field.Text("agent_instructions").Optional().Nillable().Sensitive().Comment("旧版合并指令快照：split 会话中仅作归档，legacy 会话仍可读取"),
		field.Text("project_instructions").Optional().Nillable().Sensitive().Comment("split 模式下冻结的项目指令；NULL 表示尚未冻结，空字符串表示冻结为空"),
		field.Enum("instructions_format").Values("legacy", "split").Default("legacy").Comment("legacy 保留旧合并快照；新会话显式 split，动态全局加冻结项目"),
		field.Enum("permission_mode").Values("full_approval", "auto_approval", "full_access").Default("full_access").Comment("会话级工具权限模式；未知值拒绝，旧行按兼容默认"),
		field.Int64("title_revision").Default(0).NonNegative().Comment("标题版本：自动生成与手动改名同事务递增，事件携带该值防止延迟覆盖"),
		field.Bool("favorite").Default(false),
		field.Enum("memory_mode").Values("inherit", "off", "readonly").Default("inherit").Comment("会话记忆模式：继承全局 / 关闭 / 只读；关闭 Memory 不等于不保存聊天历史"),
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
	return []ent.Edge{edge.To("turns", KaguyaChatTurn.Type), edge.To("title_jobs", KaguyaTitleJob.Type), edge.From("project", KaguyaProject.Type).Ref("conversations").Field("project_id").Unique()}
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
	return []schema.Annotation{schema.Comment(comment), entsql.Annotation{Table: table, WithComments: &enabled}, edge.Annotation{StructTag: `json:"-"`}}
}
