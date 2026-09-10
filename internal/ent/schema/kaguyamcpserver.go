package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// KaguyaMCPServer 保存 MCP 连接配置与期望启用状态，运行状态由连接管理器维护。
type KaguyaMCPServer struct{ ent.Schema }

func (KaguyaMCPServer) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100).Unique(),
		field.Enum("transport").Values("stdio", "streamable-http", "sse"),
		field.String("command").Default(""),
		field.JSON("args", []string{}).Default([]string{}),
		field.JSON("env", map[string]string{}).Default(map[string]string{}).Sensitive(),
		field.String("working_directory").Default(""),
		field.String("url").Default(""),
		field.JSON("headers", map[string]string{}).Default(map[string]string{}).Sensitive(),
		field.Int("timeout_seconds").Default(60).Min(1).Max(600),
		field.Bool("enabled").Default(false),
	}
}

func (KaguyaMCPServer) Mixin() []ent.Mixin { return (KaguyaProviderInfo{}).Mixin() }

func (KaguyaMCPServer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "kaguya_mcp_server"}}
}
