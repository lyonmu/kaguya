package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
)

// KaguyaMemoryRevision 保存页面的完整历史快照，用于审计、回滚与冲突处理；
// Revision 只存已发布状态，待审提案留在 Job.result_json。
type KaguyaMemoryRevision struct{ ent.Schema }

func (KaguyaMemoryRevision) Fields() []ent.Field {
	return []ent.Field{
		field.String("page_id").MaxLen(64).NotEmpty(),
		field.Int64("version").Positive().Comment("页面版本；恢复旧内容产生新版本，不回退版本号"),
		field.String("canonical_key").MaxLen(160).NotEmpty(),
		field.Enum("kind").Values(
			"preference", "fact", "decision", "procedure", "lesson",
		),
		field.String("title").NotEmpty(),
		field.Text("summary").Default(""),
		field.Text("body").Sensitive(),
		field.JSON("aliases", []string{}).Optional(),
		field.Enum("status").Values(
			"proposed", "active", "conflicted", "stale", "archived", "deleted",
		),
		field.Bool("pinned").Default(false),
		field.Bool("user_locked").Default(false),
		field.Time("expires_at").Optional().Nillable(),
		field.JSON("claims", []dtomemory.MemoryClaim{}).Optional().Comment("本修订的主张摘要，证据单独建表"),
		field.Enum("actor").Values("user", "task_model", "system"),
		field.String("job_id").MaxLen(64).Default("").Comment("产生该修订的作业；人工修订为空"),
		field.String("reason").Default("").Comment("变更原因"),
	}
}

func (KaguyaMemoryRevision) Mixin() []ent.Mixin { return chatHistoryMixins() }

func (KaguyaMemoryRevision) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("page_id", "version").Unique(),
	}
}

func (KaguyaMemoryRevision) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_memory_revision", "记忆页面修订快照")
}
