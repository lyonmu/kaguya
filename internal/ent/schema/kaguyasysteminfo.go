package schema

import (
	"fmt"

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
		field.Text("tls_certificate_pem").Default(""),
		field.Text("tls_private_key_pem").Default("").Sensitive(),
		field.Int("command_timeout_seconds").Default(120).Min(1).Max(86400),
		field.JSON("global_agents_paths", []string{}).Optional(),
		field.Text("system_prompt").Default("").Comment("追加到全局人设后的自定义提示词"),
		field.String("user_agent").MaxLen(512).Default(consts.DefaultUserAgent).Comment("出站模型 API 请求的 User-Agent"),
		field.String("default_model_id").Default("").Comment("默认聊天模型的本地记录 ID，空值表示未配置"),
		field.String("task_model_id").Default("").Comment("后台任务模型的本地记录 ID，空值表示未配置"),
	}
}

func (KaguyaSystemInfo) Mixin() []ent.Mixin { return []ent.Mixin{pkg.TimeMixin{}} }
func (KaguyaSystemInfo) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_system_info", "全局系统配置")
}
