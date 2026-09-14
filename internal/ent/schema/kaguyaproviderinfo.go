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
		field.String("provider_name").NotEmpty().Comment("提供商名称"),
		field.String("provider_type").GoType(consts.ProviderType("")).Default(string(consts.ProviderTypeNormal)).Comment("提供商类型：normal 或 opencode-go"),
		field.String("api_key").Optional().Comment("API Key"),
		field.String("base_url").Optional().Comment("API 版本根地址，请求路径由所属模型的协议追加"),
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
		// 唯一性只约束未删除提供商：软删除的行保留历史，不阻止同名重建。
		index.Fields("provider_name").Unique().Annotations(entsql.IndexWhere("deleted_at IS NULL")),
	}
}

func (KaguyaProviderInfo) Annotations() []schema.Annotation {
	withCommentsEnabled := true
	return []schema.Annotation{
		schema.Comment("模型提供商信息表"),
		entsql.Annotation{
			Table:        "kaguya_provider_info",
			WithComments: &withCommentsEnabled,
		},
		edge.Annotation{StructTag: `json:"-"`},
	}
}
