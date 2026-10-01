package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaTitleJob 是每会话一行的标题生成 FIFO 队列：数据库是权威积压，
// 内存 wake channel 只作通知。排队不保存 API Key 或完整 ProviderConfig。
type KaguyaTitleJob struct{ ent.Schema }

func (KaguyaTitleJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("conversation_id").MaxLen(64).NotEmpty(),
		field.Int64("enqueue_order").Comment("FIFO 顺序，使用入队时 global.Id.GenID() 的数值；终态重试更新顺序"),
		field.Enum("status").Values("pending", "running", "succeeded", "failed", "canceled").Default("pending"),
		field.Text("question").Comment("标题摘要输入，服务层限制 4000 rune，不保存密钥"),
		field.Text("answer").Default("").Comment("可选的助手回答开头，服务层限制 4000 rune"),
		field.Int("attempt_count").Default(0).NonNegative(),
		field.Time("started_at").Optional().Nillable().Comment("worker 领取时间；排队等待不侵占 30 秒执行预算"),
		field.Time("finished_at").Optional().Nillable(),
		field.String("error_code").Default("").Comment("安全错误码，不保存原始错误文本"),
		field.Int64("revision").Default(0).NonNegative().Comment("任务自身版本；会话标题版本以 conversation.title_revision 为准"),
	}
}

func (KaguyaTitleJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("conversation", KaguyaConversation.Type).Ref("title_jobs").Field("conversation_id").Unique().Required(),
	}
}

func (KaguyaTitleJob) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaTitleJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("conversation_id").Unique(),
		index.Fields("status", "enqueue_order"),
	}
}

func (KaguyaTitleJob) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_title_job", "标题生成 FIFO 队列")
}
