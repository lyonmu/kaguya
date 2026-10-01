package permission

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// shellReadOnlyCommands 是第一版可信只读白名单：仅放行不解析 PATH、不带任何
// 参数与重定向的 shell 内建命令。外部命令（含 git）在执行 profile 能保证
// 固定可执行文件、清除 BASH_ENV/导出函数/Git 外部程序与钩子之前一律不放行。
var shellReadOnlyCommands = map[string]bool{
	"pwd":  true,
	"true": true,
}

// ShellClassification 用固定版本的 AST 解析器分析 bash 命令，只对字面量、
// 无重定向/管道/替换/函数/变量/env/eval/exec/subshell 的白名单命令返回
// read_only；解析失败或不在白名单一律 unknown。AST 解析成功不代表安全，
// 任何未验证形态都必须审批。
func ShellClassification(command string) Classification {
	class := Classification{Identity: "builtin:bash", Effect: EffectUnknown}
	if strings.TrimSpace(command) == "" {
		return class
	}
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil || file == nil || len(file.Stmts) != 1 {
		return class
	}
	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 || stmt.Semicolon.IsValid() {
		return class
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return class
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		value, ok := literalWord(word)
		if !ok {
			return class
		}
		args = append(args, value)
	}
	if len(args) != 1 || !shellReadOnlyCommands[args[0]] {
		return class
	}
	return Classification{Identity: "builtin:bash", Effect: EffectReadOnly, ReadOnlyProven: true}
}

// literalWord 只接受单个无引号、无展开的字面量部分；引号、变量、命令替换、
// 算术、进程替换、扩展 glob 一律拒绝。
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	return lit.Value, true
}
