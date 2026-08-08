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

// ReasoningEffort 定义模型思考/推理的努力程度
type ReasoningEffort string

const (
	// ReasoningEffortLow 低强度思考，响应更快但推理深度较浅
	ReasoningEffortLow ReasoningEffort = "low"
	// ReasoningEffortMedium 中等强度思考，平衡速度与推理深度
	ReasoningEffortMedium ReasoningEffort = "medium"
	// ReasoningEffortHigh 高强度思考，推理更深入但响应较慢
	ReasoningEffortHigh ReasoningEffort = "high"
)

// KaguyaModelsInfo holds the schema definition for the KaguyaModelsInfo entity.
type KaguyaModelsInfo struct {
	ent.Schema
}

// Fields of the KaguyaModelsInfo.
func (KaguyaModelsInfo) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider_id").Optional().Comment("提供商id"),
		field.String("model_name").Optional().Comment("模型显示名称"),
		field.String("model_id").Optional().Comment("调用 API 时使用的模型标识符"),
		field.Int("is_default").Optional().GoType(consts.Status(0)).Default(int(consts.IsTrue)).Comment("是否为该提供方下的默认模型"),
		field.Int("reasoning_enabled").Optional().GoType(consts.Status(0)).Default(int(consts.IsTrue)).Comment("是否启用思考模式"),
		field.String("reasoning_effort").Optional().GoType(ReasoningEffort("")).Default(string(ReasoningEffortMedium)).Comment("思考努力程度，影响推理深度和响应速度"),
		field.Int("token_context_window").Optional().Comment("模型支持的最大上下文窗口大小（token 数）"),
		field.Int("token_max_output_tokens").Optional().Comment("模型单次生成的最大输出 token 数"),
		field.Int("capability_tool_use").Optional().GoType(consts.Status(0)).Default(int(consts.IsTrue)).Comment("是否支持工具调用（function calling）"),
		field.Int("capability_vision").Optional().GoType(consts.Status(0)).Default(int(consts.IsTrue)).Comment("是否支持图像理解"),
		field.Int("capability_structured_output").Optional().GoType(consts.Status(0)).Default(int(consts.IsTrue)).Comment("是否支持结构化输出（如 JSON schema 约束）"),
	}
}

// Edges of the KaguyaModelsInfo.
func (KaguyaModelsInfo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("provider", KaguyaProviderInfo.Type).
			Ref("models").
			Field("provider_id").
			Unique().
			Comment("所属模型提供商"),
	}
}

func (KaguyaModelsInfo) Mixin() []ent.Mixin {
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

func (KaguyaModelsInfo) Indexes() []ent.Index {

	return []ent.Index{
		index.Fields("provider_id"),
	}
}

func (KaguyaModelsInfo) Annotations() []schema.Annotation {
	withCommentsEnabled := true
	return []schema.Annotation{
		schema.Comment("模型信息表"),
		entsql.Annotation{
			Table:        "kaguya_models_info",
			Charset:      "utf8mb4",
			Collation:    "utf8mb4_general_ci",
			WithComments: &withCommentsEnabled,
		},
		edge.Annotation{StructTag: `json:"-" gorm:"-"`},
	}
}
