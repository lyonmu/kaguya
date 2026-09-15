package schema

import (
	"fmt"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/pkg"
)

// KaguyaSystemInfo 是全局单例配置；模型 ID 指向本地模型记录，不是上游 API 名称。
type KaguyaSystemInfo struct{ ent.Schema }

func (KaguyaSystemInfo) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Default(consts.SystemInfoID).Immutable().Validate(func(id string) error {
			if id != consts.SystemInfoID {
				return fmt.Errorf("system info ID must be global")
			}
			return nil
		}),
		field.Int("agent_max_steps").Default(0).Min(0).Max(1000),
		field.Int("context_compaction_percent").Default(90).Min(10).Max(95),
		field.Int("command_timeout_seconds").Default(120).Min(1).Max(86400),
		field.Int("chat_max_retries").Default(5).Min(0).Max(20).Comment("聊天模型流式请求的最大重试次数；0 表示禁用"),
		field.JSON("global_agents_paths", []string{}).Optional(),
		field.Text("global_system_prompt").Default(consts.GlobalSystemPrompt).Comment("可编辑的全局基础提示词"),
		field.Text("system_prompt").Default("").Comment("追加到全局人设后的自定义提示词"),
		field.Bool("model_sync_enabled").Default(false).Comment("是否定时同步 models.dev 目录"),
		field.String("provider_sync_url").Default(consts.DefaultProviderCatalogURL).MaxLen(2048).Comment("提供商目录同步地址（api.json）"),
		field.String("model_sync_url").Default(consts.DefaultModelCatalogURL).MaxLen(2048).Comment("模型目录同步地址（models.json）"),
		field.Int("model_sync_interval_hours").Default(24).Min(1).Max(720).Comment("模型目录同步间隔小时数"),
		field.Text("model_catalog_json").Default("[]").Comment("models.dev 模型目录缓存，不通过系统配置接口返回"),
		field.Int("model_catalog_count").Default(0).Min(0).Comment("已缓存的模型目录条目数"),
		field.Text("provider_catalog_json").Default("[]").Comment("models.dev 提供商目录缓存，只包含有 api 字段的提供商"),
		field.Int("provider_catalog_count").Default(0).Min(0).Comment("已缓存的提供商目录条目数"),
		field.Time("model_sync_last_attempt_at").Optional().Nillable().GoType(time.Time{}),
		field.Time("model_sync_last_success_at").Optional().Nillable().GoType(time.Time{}),
		field.Text("model_sync_last_error").Default(""),
		field.String("default_model_id").Default("").Comment("默认聊天模型的本地记录 ID，空值表示未配置"),
		field.String("task_model_id").Default("").Comment("后台任务模型的本地记录 ID，空值表示未配置"),
	}
}

func (KaguyaSystemInfo) Mixin() []ent.Mixin { return []ent.Mixin{pkg.TimeMixin{}} }
func (KaguyaSystemInfo) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_system_info", "全局系统配置")
}
