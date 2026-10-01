package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaMCPServer 保存 MCP 连接配置与期望启用状态，运行状态由连接管理器维护。
type KaguyaMCPServer struct{ ent.Schema }

func (KaguyaMCPServer) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.Enum("transport").Values("stdio", "streamable-http", "sse"),
		field.String("command").Default(""),
		field.JSON("args", []string{}).Default([]string{}),
		field.JSON("env", map[string]string{}).Default(map[string]string{}).Sensitive(),
		field.String("working_directory").Default(""),
		field.String("url").Default(""),
		field.JSON("headers", map[string]string{}).Default(map[string]string{}).Sensitive(),
		field.Int("timeout_seconds").Default(60).Min(1).Max(600),
		field.Bool("enabled").Default(false),
		field.Int("reconnect_failures").Default(0).NonNegative().Comment("当前连接周期内 Prepare 连续失败次数"),
		field.Bool("reconnect_circuit_open").Default(false).Comment("熔断是否打开；重启后保持，不由启动恢复自动清除"),
		field.Int64("reconnect_cycle").Default(0).NonNegative().Comment("手动重连开启的新连接周期"),
		field.Time("reconnect_next_at").Optional().Nillable().Comment("下一次自动重连时间；熔断/open 或运行中为空"),
		field.Time("reconnect_opened_at").Optional().Nillable().Comment("熔断打开时间"),
	}
}

func (KaguyaMCPServer) Mixin() []ent.Mixin { return (KaguyaProviderInfo{}).Mixin() }

func (KaguyaMCPServer) Indexes() []ent.Index {
	// 唯一性只约束未删除服务：软删除的行保留历史，不阻止同名重建。
	return []ent.Index{index.Fields("name").Unique().Annotations(entsql.IndexWhere("deleted_at IS NULL"))}
}

func (KaguyaMCPServer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "kaguya_mcp_server"}}
}
