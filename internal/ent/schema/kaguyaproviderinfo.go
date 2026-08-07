package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// ProviderProtocol 定义模型服务提供方的 API 协议类型
type ProviderProtocol string

const (
	// ProtocolOpenAI 表示兼容 OpenAI 的 API 协议
	ProtocolOpenAI ProviderProtocol = "openai"
	// ProtocolAnthropic 表示 Anthropic 原生 API 协议
	ProtocolAnthropic ProviderProtocol = "anthropic"
)

// KaguyaProviderInfo holds the schema definition for the KaguyaProviderInfo entity.
type KaguyaProviderInfo struct {
	ent.Schema
}

// Fields of the KaguyaProviderInfo.
func (KaguyaProviderInfo) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider_name").Unique().Optional().Comment("提供商名称"),
		field.String("api_protocol").Optional().GoType(ProviderProtocol("")).Optional().Comment("API 协议类型").Default(string(ProtocolOpenAI)),
		field.String("api_key").Optional().Comment("API Key"),
		field.String("base_url").Optional().Comment("Base URL"),
	}

}

// Edges of the KaguyaProviderInfo.
func (KaguyaProviderInfo) Edges() []ent.Edge {
	return nil
}

func (KaguyaProviderInfo) Mixin() []ent.Mixin {
	return []ent.Mixin{
		pkg.NewIDMixin(func() string {
			id, err := global.Id.GenID()
			if err != nil {
				panic(fmt.Sprintf("failed to generate ID: %v", err))
			}
			return fmt.Sprintf("%d", id)
		}),
		pkg.TimeMixin{},
	}
}

func (KaguyaProviderInfo) Indexes() []ent.Index {
	return []ent.Index{}
}

func (KaguyaProviderInfo) Annotations() []schema.Annotation {
	withCommentsEnabled := true
	return []schema.Annotation{
		schema.Comment("模型提供商信息表"),
		entsql.Annotation{
			Table:        "kaguya_provider_info",
			Charset:      "utf8mb4",
			Collation:    "utf8mb4_general_ci",
			WithComments: &withCommentsEnabled,
		},
		edge.Annotation{StructTag: `json:"-" gorm:"-"`},
	}
}
