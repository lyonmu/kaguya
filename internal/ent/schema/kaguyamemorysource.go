package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMemorySource 是长期记忆的来源引用，兼任持久化 outbox：
// completed 轮次与用户笔记在写入来源后由后台 Worker 兑现，重启不丢任务。
type KaguyaMemorySource struct{ ent.Schema }

func (KaguyaMemorySource) Fields() []ent.Field {
	return []ent.Field{
		field.String("source_key").MaxLen(200).NotEmpty().Comment("稳定来源键，如 turn:<turn-id>:projection-v1；唯一约束防止重复入队"),
		field.Enum("kind").Values("turn", "note", "import").Default("turn").Comment("来源类型：完成轮次 / 用户笔记 / 显式导入资料"),
		field.String("scope_key").MaxLen(128).NotEmpty().Comment("personal / shared / project:<project-id>，来源创建后不改写"),
		field.String("conversation_id").MaxLen(64).Default("").Comment("来源会话；笔记等无会话来源为空"),
		field.String("turn_id").MaxLen(64).Default("").Comment("来源轮次"),
		field.Int("projection_version").Default(1).Positive().Comment("来源投影版本，投影契约变化时递增"),
		field.Int("cursor_part").Default(0).NonNegative().Comment("投影 segment 游标：超预算切分后剩余部分仍待处理"),
		field.String("content_hash").MaxLen(64).Default("").Comment("投影内容哈希，用于排除与去重；不保证拦截同义改写"),
		field.Text("raw_content").Sensitive().Default("").Comment("显式导入资料的稳定快照；turn/note 来源为空，Worker 从轮次或页面正文重建投影"),
		field.String("document_path").MaxLen(4096).Default("").Comment("导入资料的项目内相对路径，仅用于来源导航"),
		field.Enum("state").Values("pending", "claimed", "processed", "noop", "failed", "excluded").
			Default("pending").Comment("pending→claimed→processed/noop/failed；excluded 表示隐私关闭、删除或来源失效"),
		field.String("job_id").MaxLen(64).Default("").Comment("当前/最近领取该来源的作业"),
		field.Time("captured_at").Comment("来源捕获时间"),
		field.Int64("policy_epoch").Default(0).NonNegative().Comment("捕获时的记忆策略版本，作为并发栅栏"),
	}
}

func (KaguyaMemorySource) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemorySource) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_key").Unique(),
		index.Fields("state", "captured_at"),
		index.Fields("conversation_id"),
		index.Fields("job_id"),
	}
}

func (KaguyaMemorySource) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_source", "长期记忆来源与持久化待处理队列")
}
