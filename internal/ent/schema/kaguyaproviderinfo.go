package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// KaguyaProviderInfo holds the schema definition for the KaguyaProviderInfo entity.
type KaguyaProviderInfo struct {
	ent.Schema
}

// Fields of the KaguyaProviderInfo.
func (KaguyaProviderInfo) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider_name").Unique().Optional().Comment("提供商名称"),
		field.String("api_protocol").Optional().GoType(consts.ProviderProtocol("")).Optional().Comment("API 协议类型").Default(string(consts.ProtocolOpenAICompletions)),
		field.String("api_key").Optional().Comment("API Key"),
		field.String("base_url").Optional().Comment("Base URL"),
	}

}

// Edges of the KaguyaProviderInfo.
func (KaguyaProviderInfo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("models", KaguyaModelsInfo.Type).
			Comment("该提供方下的模型列表"),
	}
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
	return []ent.Index{
		index.Fields("provider_name"),
		index.Fields("api_protocol"),
	}
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
