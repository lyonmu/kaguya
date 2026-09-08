package schema

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KaguyaChatBlock 按首次出现顺序记录思考/正文/工具；工具输入输出合并一行。
type KaguyaChatBlock struct{ ent.Schema }

func (KaguyaChatBlock) Fields() []ent.Field {
	return []ent.Field{
		field.String("turn_id").MaxLen(64).NotEmpty(),
		field.Int64("sequence").Positive().Comment("轮内首次出现顺序，非时间戳排序"),
		field.Enum("type").Values("text", "reasoning", "tool_call"),
		field.Text("text").Default("").SchemaType(map[string]string{dialect.MySQL: "longtext"}),
		field.String("tool_call_id").Default(""), field.String("tool_name").Default(""),
		field.Text("input").Default("").SchemaType(map[string]string{dialect.MySQL: "longtext"}),
		field.JSON("output", json.RawMessage{}).Optional(),
		field.Bool("provider_executed").Default(false), field.Bool("is_error").Default(false),
		field.Text("error_message").Default(""),
		field.Time("started_at"), field.Time("finished_at"),
		field.Int64("start_order").Positive(), field.Int64("end_order").Positive().Comment("回调事件序号，保留并行工具的真实完成先后"),
	}
}
func (KaguyaChatBlock) Edges() []ent.Edge {
	return []ent.Edge{edge.From("turn", KaguyaChatTurn.Type).Ref("blocks").Field("turn_id").Unique().Required()}
}
func (KaguyaChatBlock) Mixin() []ent.Mixin { return chatHistoryMixins() }
func (KaguyaChatBlock) Indexes() []ent.Index {
	return []ent.Index{index.Fields("turn_id", "sequence").Unique()}
}
func (KaguyaChatBlock) Annotations() []schema.Annotation {
	return chatHistoryAnnotations("kaguya_chat_block", "按执行顺序保存的完整模型内容块")
}
