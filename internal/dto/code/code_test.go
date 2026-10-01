package code

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scanResponseCodes 解析本包源码中所有具名 Response 字面量，返回“变量名 → 码值”。
func scanResponseCodes(t *testing.T) map[string]int {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]int)
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok != token.VAR {
				return true
			}
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expr := range value.Values {
					lit, ok := expr.(*ast.CompositeLit)
					if !ok || !isResponseLiteral(lit) || index >= len(value.Names) {
						continue
					}
					number, found := responseCode(lit)
					if !found {
						t.Errorf("%s: response code must be an integer literal", fset.Position(lit.Pos()))
						continue
					}
					codes[value.Names[index].Name] = number
				}
			}
			return true
		})
	}
	if len(codes) == 0 {
		t.Fatal("no response codes found")
	}
	return codes
}

func responseCode(lit *ast.CompositeLit) (int, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Code" {
			continue
		}
		value, ok := kv.Value.(*ast.BasicLit)
		if !ok || value.Kind != token.INT {
			return 0, false
		}
		number, err := strconv.Atoi(value.Value)
		if err != nil {
			return 0, false
		}
		return number, true
	}
	return 0, false
}

func isResponseLiteral(lit *ast.CompositeLit) bool {
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == "Response"
}

// 业务码按域分段且必须唯一；解析本包源码，新增 Response 定义会自动纳入校验。
func TestResponseCodesAreUnique(t *testing.T) {
	codes := scanResponseCodes(t)
	seen := make(map[int]string)
	for name, number := range codes {
		if previous, exists := seen[number]; exists {
			t.Errorf("duplicate response code %d: %s and %s", number, previous, name)
			continue
		}
		seen[number] = name
	}
	if SystemSuccess.Code != 100000 {
		t.Fatalf("success code changed: %d", SystemSuccess.Code)
	}
}

// 域区间与正值：传输层 100xxx、聊天 102xxx、模型 103xxx、系统 104xxx、
// MCP 105xxx、提供商 106xxx、项目 107xxx、记忆 108xxx；999999 为兜底失败。
func TestResponseCodeRanges(t *testing.T) {
	allowed := map[int]bool{100: true, 102: true, 103: true, 104: true, 105: true, 106: true, 107: true, 108: true}
	for name, number := range scanResponseCodes(t) {
		if number <= 0 {
			t.Errorf("%s: response code must be positive, got %d", name, number)
			continue
		}
		if number < 100000 || number > 999999 {
			t.Errorf("%s: response code %d is out of the six-digit range", name, number)
			continue
		}
		if number == SystemFailure.Code {
			continue
		}
		if !allowed[number/1000] {
			t.Errorf("%s: response code %d is outside the defined domain ranges", name, number)
		}
	}
}

// 固定旧码：数字、语义和 fallback 必须保持，任何改动都是兼容性破坏。
func TestOldResponseCodesAreStable(t *testing.T) {
	fixed := map[string]int{
		"SystemSuccess": 100000, "SystemFailure": 999999, "RequestParameterError": 100001,
		"ChatSSEFailure": 102000, "ChatBusy": 102002, "ConversationNotFound": 102003,
		"ConversationQueryFailure": 102004, "ConversationUpdateFailure": 102005,
		"ConversationDeleteFailure": 102006, "TaskModelNotConfigured": 102007,
		"ChatModelNotConfigured": 102008, "ChatConcurrencyLimited": 102009,
		"ProviderSecretUnusable": 102010,
		"ModelQueryFailure":      103000, "ModelNotFound": 103001, "ModelCreateFailure": 103002,
		"ModelUpdateFailure": 103003, "ModelDeleteFailure": 103004, "ModelIDAlreadyExist": 103005,
		"ModelCatalogSyncFailure": 103006, "ModelTestFailure": 103007,
		"SystemInfoQueryFailure": 104000, "SystemInfoUpdateFailure": 104001,
		"MCPFailure": 105000, "MCPNotFound": 105001, "MCPDuplicate": 105002,
		"MCPConnectFailure": 105003, "MCPConfigInvalid": 105004,
		"ProviderQueryFailure": 106000, "ProviderNotFound": 106001, "ProviderCreateFailure": 106002,
		"ProviderUpdateFailure": 106003, "ProviderDeleteFailure": 106004,
		"ProviderNameAlreadyExist": 106005, "ProviderSecretInvalid": 106006,
		"ProjectOperationFailure": 107000, "ProjectNotFound": 107001,
		"ProjectParameterError": 107002, "ProjectPathInUse": 107003, "ProjectNotGit": 107004,
		"MemoryFailure": 108000, "MemoryQueryFailure": 108001, "MemoryPageNotFound": 108002,
		"MemoryVersionConflict": 108003, "MemoryJobNotFound": 108004, "MemoryJobNotReview": 108005,
		"MemoryDisabled": 108006, "MemorySourceNotFound": 108007,
	}
	codes := scanResponseCodes(t)
	for name, want := range fixed {
		got, ok := codes[name]
		if !ok {
			t.Errorf("%s: response definition is missing", name)
			continue
		}
		if got != want {
			t.Errorf("%s: code changed from %d to %d", name, want, got)
		}
	}
}

// 102009 已保留，不得被新语义复用。
func TestReservedChatConcurrencyCode(t *testing.T) {
	if ChatConcurrencyLimited.Code != 102009 {
		t.Fatalf("reserved code changed: %d", ChatConcurrencyLimited.Code)
	}
	used := make(map[int]string)
	for name, number := range scanResponseCodes(t) {
		if number != 102009 {
			continue
		}
		if name != "ChatConcurrencyLimited" {
			t.Errorf("reserved code 102009 reused by %s", name)
		}
		used[number] = name
	}
	if len(used) == 0 {
		t.Fatal("reserved code 102009 is no longer defined")
	}
}

// 前端常量由 scripts/generate-api-codes.go 生成；源码变更后必须同步。
func TestFrontendCodesAreSynced(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "api", "codes.ts"))
	if err != nil {
		t.Fatalf("read generated frontend codes: %v", err)
	}
	frontend := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ":") || strings.HasPrefix(line, "//") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), ","))
		number, convErr := strconv.Atoi(value)
		if convErr != nil || name == "export const CODES" || name == "" {
			continue
		}
		frontend[name] = number
	}
	backend := scanResponseCodes(t)
	for name, number := range backend {
		got, ok := frontend[name]
		if !ok {
			t.Errorf("frontend codes.ts is missing %s", name)
			continue
		}
		if got != number {
			t.Errorf("frontend codes.ts has %s=%d, backend has %d", name, got, number)
		}
	}
	if len(frontend) != len(backend) {
		t.Errorf("frontend codes.ts has %d entries, backend has %d; regenerate with: go run scripts/generate-api-codes.go", len(frontend), len(backend))
	}
}
