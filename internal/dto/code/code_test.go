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

// 业务码按域分段且必须唯一；解析本包源码，新增 Response 定义会自动纳入校验。
func TestResponseCodesAreUnique(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]string)
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
			lit, ok := node.(*ast.CompositeLit)
			if !ok || !isResponseLiteral(lit) {
				return true
			}
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
					t.Errorf("%s: response code must be an integer literal", fset.Position(kv.Value.Pos()))
					continue
				}
				number, convErr := strconv.Atoi(value.Value)
				if convErr != nil {
					t.Errorf("%s: parse response code: %v", fset.Position(value.Pos()), convErr)
					continue
				}
				position := fset.Position(lit.Pos()).String()
				if previous, exists := seen[number]; exists {
					t.Errorf("duplicate response code %d: %s and %s", number, previous, position)
					continue
				}
				seen[number] = position
			}
			return true
		})
	}
	if len(seen) == 0 {
		t.Fatal("no response codes found")
	}
	if _, ok := seen[SystemSuccess.Code]; !ok {
		t.Fatalf("success response code %d is not registered in source", SystemSuccess.Code)
	}
	if SystemSuccess.Code != 100000 {
		t.Fatalf("success code changed: %d", SystemSuccess.Code)
	}
}

func isResponseLiteral(lit *ast.CompositeLit) bool {
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == "Response"
}
