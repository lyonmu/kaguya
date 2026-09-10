package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/schema"
	"github.com/kaptinlin/jsonschema"
)

// Fantasy's Go reflection emits types/required/descriptions, but not bounds or
// array lengths. Add the actual execution constraints before exposing metadata
// and compile once per tool instead of compiling on every invocation.
type inputContractTool struct {
	fantasy.AgentTool
	definition []byte
	validator  *jsonschema.Schema
}

func withInputContract(base fantasy.AgentTool, generated schema.Schema) fantasy.AgentTool {
	definition := schema.ToMap(generated)
	props := definition["properties"].(map[string]any)
	property := func(name string) map[string]any { return props[name].(map[string]any) }
	switch base.Info().Name {
	case "read":
		property("path")["minLength"] = 1
		property("offset")["minimum"] = 1
		property("limit")["minimum"] = 1
	case "bash":
		property("command")["minLength"] = 1
		property("command")["pattern"] = `\S`
		property("timeout")["exclusiveMinimum"] = 0
		property("timeout")["maximum"] = 2147483.647
	case "edit":
		property("path")["minLength"] = 1
		property("edits")["minItems"] = 1
		item := property("edits")["items"].(map[string]any)
		item["additionalProperties"] = false
		item["properties"].(map[string]any)["oldText"].(map[string]any)["minLength"] = 1
	case "write":
		property("path")["minLength"] = 1
	case "ls", "find", "grep":
		property("limit")["minimum"] = 1
		property("limit")["maximum"] = 100000
		if base.Info().Name == "find" {
			property("pattern")["minLength"] = 1
		}
		if base.Info().Name == "grep" {
			property("context")["minimum"] = 0
			property("context")["maximum"] = 1000
		}
	}
	// Fantasy reconstructs the root using only properties/required. Keep this
	// extra root check local; descriptions also tell models to use declared keys.
	definition["additionalProperties"] = false
	raw, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Sprintf("encode built-in tool schema: %v", err))
	}
	validator, err := jsonschema.NewCompiler().Compile(raw)
	if err != nil {
		panic(fmt.Sprintf("compile built-in tool schema: %v", err))
	}
	return &inputContractTool{AgentTool: base, definition: raw, validator: validator}
}

func (t *inputContractTool) Info() fantasy.ToolInfo {
	// Providers may normalize nested schemas in place; return a fresh tree.
	var definition map[string]any
	if err := json.Unmarshal(t.definition, &definition); err != nil {
		panic(err)
	}
	info := t.AgentTool.Info()
	info.Parameters = definition["properties"].(map[string]any)
	return info
}

func (t *inputContractTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if err := ctx.Err(); err != nil {
		return fantasy.ToolResponse{}, err
	}
	var input any
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: expected one valid JSON object using this tool's declared fields; do not send Markdown, a JSON string, or shell arguments"), nil
	}
	result := t.validator.Validate(input)
	if !result.IsValid() {
		var details []string
		for path, message := range result.DetailedErrors() {
			details = append(details, path+": "+message)
		}
		sort.Strings(details)
		return fantasy.NewTextErrorResponse("invalid parameters for " + t.AgentTool.Info().Name + ": " + strings.Join(details, "; ") + ". Correct these fields using the tool schema before retrying; no action was executed."), nil
	}
	// Preserve the upstream call ID; it correlates tool results with model calls.
	return t.AgentTool.Run(ctx, call)
}
