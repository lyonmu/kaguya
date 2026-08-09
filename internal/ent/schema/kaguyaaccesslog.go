package schema

import (
	"fmt"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// KaguyaAccessLog holds the schema definition for the KaguyaAccessLog entity.
type KaguyaAccessLog struct {
	ent.Schema
}

// Fields of the KaguyaAccessLog.
func (KaguyaAccessLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("access_ip").Optional().Comment("访问IP"),
		field.Int64("access_time").Optional().Comment("操作时间").DefaultFunc(func() int64 { return time.Now().Unix() }),
		field.String("os").Optional().Comment("操作系统"),
		field.String("platform").Optional().Comment("操作平台"),
		field.String("browser_name").Optional().Comment("浏览器名称"),
		field.String("browser_version").Optional().Comment("浏览器版本"),
		field.String("browser_engine_name").Optional().Comment("浏览器引擎名称"),
		field.String("browser_engine_version").Optional().Comment("浏览器引擎版本"),
	}
}

// Edges of the KaguyaAccessLog.
func (KaguyaAccessLog) Edges() []ent.Edge {
	return nil
}

func (KaguyaAccessLog) Mixin() []ent.Mixin {
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

func (KaguyaAccessLog) Indexes() []ent.Index {

	return []ent.Index{
		index.Fields("access_ip"),
		index.Fields("access_time"),
	}
}

func (KaguyaAccessLog) Annotations() []schema.Annotation {
	withCommentsEnabled := true
	return []schema.Annotation{
		schema.Comment("访问日志信息表"),
		entsql.Annotation{
			Table:        "kaguya_access_log",
			Charset:      "utf8mb4",
			Collation:    "utf8mb4_general_ci",
			WithComments: &withCommentsEnabled,
		},
		edge.Annotation{StructTag: `json:"-" gorm:"-"`},
	}
}
