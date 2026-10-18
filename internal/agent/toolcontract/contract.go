// Package toolcontract 统一内置工具与记忆工具的 JSON schema 校验装饰：
// Info 返回防御性拷贝的参数字典，Run 在执行前拒绝未知字段与越界值。
// 它只依赖 fantasy 与 jsonschema，不 import service 包。
package toolcontract

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/schema"
	"github.com/kaptinlin/jsonschema"
)

// Tool 在原始 AgentTool 之上附加已编译的参数校验器。
type Tool struct {
	fantasy.AgentTool
	definition []byte
	validator  *jsonschema.Schema
	identity   string
}

// WithIdentity 由本机工具工厂在注册时绑定身份，不从 Info().Name 推断权限。
// 返回独立装饰器，保持 schema 和 provider options 的既有委托方式。
func WithIdentity(base fantasy.AgentTool, identity string) fantasy.AgentTool {
	tool, ok := base.(*Tool)
	if !ok || identity == "" {
		panic("tool identity requires a schema contract and a nonempty identity")
	}
	copy := *tool
	copy.identity = identity
	return &copy
}

func (t *Tool) PermissionIdentity() string { return t.identity }

// Wrap 编译调用方提供的完整 JSON schema 定义；additionalProperties 统一关闭。
func Wrap(base fantasy.AgentTool, definition map[string]any) fantasy.AgentTool {
	// Fantasy reconstructs the root using only properties/required. Keep this
	// extra root check local; descriptions also tell models to use declared keys.
	definition["additionalProperties"] = false
	raw, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Sprintf("encode tool schema %q: %v", base.Info().Name, err))
	}
	return WrapRaw(base, raw)
}

// WrapGenerated 从 Go 输入类型反射生成 schema 后包装，供简单参数结构使用。
func WrapGenerated[T any](base fantasy.AgentTool) fantasy.AgentTool {
	var input T
	return Wrap(base, schema.ToMap(schema.Generate(reflect.TypeOf(input))))
}

// WrapRaw 用已序列化的 schema 包装工具；编译失败是编程错误。
func WrapRaw(base fantasy.AgentTool, definition []byte) fantasy.AgentTool {
	validator, err := jsonschema.NewCompiler().Compile(definition)
	if err != nil {
		panic(fmt.Sprintf("compile tool schema %q: %v", base.Info().Name, err))
	}
	return &Tool{AgentTool: base, definition: definition, validator: validator}
}

// Validator 暴露已编译校验器，供契约测试断言示例参数。
func (t *Tool) Validator() *jsonschema.Schema { return t.validator }

func (t *Tool) Info() fantasy.ToolInfo {
	// Providers may normalize nested schemas in place; return a fresh tree.
	var definition map[string]any
	if err := json.Unmarshal(t.definition, &definition); err != nil {
		panic(err)
	}
	info := t.AgentTool.Info()
	info.Parameters = definition["properties"].(map[string]any)
	return info
}

// ValidateInput 只校验参数形状，不执行工具或访问工作区，供审批前使用。
func (t *Tool) ValidateInput(call fantasy.ToolCall) string {
	var input any
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return "invalid parameters: expected one valid JSON object using this tool's declared fields; do not send Markdown, a JSON string, or shell arguments"
	}
	result := t.validator.Validate(input)
	if !result.IsValid() {
		var details []string
		for path, message := range result.DetailedErrors() {
			details = append(details, path+": "+message)
		}
		sort.Strings(details)
		return "invalid parameters for " + t.AgentTool.Info().Name + ": " + strings.Join(details, "; ") + ". Correct these fields using the tool schema before retrying; no action was executed."
	}
	return ""
}

func (t *Tool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if err := ctx.Err(); err != nil {
		return fantasy.ToolResponse{}, err
	}
	if message := t.ValidateInput(call); message != "" {
		return fantasy.NewTextErrorResponse(message), nil
	}
	// Preserve the upstream call ID; it correlates tool results with model calls.
	return t.AgentTool.Run(ctx, call)
}
