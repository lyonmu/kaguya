package tools

import (
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
	"charm.land/fantasy/schema"
	"github.com/lyonmu/kaguya/internal/agent/toolcontract"
)

// Fantasy's Go reflection emits types/required/descriptions, but not bounds or
// array lengths. Add the actual execution constraints before exposing metadata
// and compile once per tool instead of compiling on every invocation.
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
	return toolcontract.WithIdentity(toolcontract.WrapRaw(base, raw), "builtin:"+base.Info().Name)
}
