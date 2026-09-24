package memory

import (
	"regexp"
	"strings"
	"testing"
)

// phrasePattern 匹配 FTS 字面短语（内部 "" 是转义引号）。
var phrasePattern = regexp.MustCompile(`"(?:[^"]|"")*"`)

func TestNormalizeFTS(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"中文", "数据库加密", "数 据 库 加 密"},
		{"混合", "SQLCipher 支持数据库加密", "SQLCipher 支 持 数 据 库 加 密"},
		{"标识符", "go-sqlite3 与 context_messages", "go-sqlite3 与 context_messages"},
		{"空白折叠", "  加 密  \n 数据 ", "加 密 数 据"},
		{"日文假名", "メモリー", "メ モ リ ー"},
	} {
		if got := NormalizeFTS(tc.in); got != tc.want {
			t.Fatalf("%s: NormalizeFTS(%q)=%q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestLiteralFTSPhraseEscapesQuotes(t *testing.T) {
	if got := LiteralFTSPhrase(`加"密`); got != `"加 "" 密"` {
		t.Fatalf("escape: %q", got)
	}
	if got := LiteralFTSPhrase("   "); got != "" {
		t.Fatalf("blank: %q", got)
	}
	// 标点保持在短语里，由 FTS 按索引相同的规则切词，不拼接不存在的新词。
	if got := LiteralFTSPhrase("go-sqlite3"); got != `"go-sqlite3"` {
		t.Fatalf("identifier: %q", got)
	}
}

func TestBuildFTSQuery(t *testing.T) {
	t.Run("空查询不构造隐式全表查询", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "?!。", "a", `"`} {
			if query, ok := BuildFTSQuery(raw); ok {
				t.Fatalf("raw %q must not produce query %q", raw, query)
			}
		}
	})
	t.Run("两字中文词", func(t *testing.T) {
		query, ok := BuildFTSQuery("加密")
		if !ok || query != `"加 密"` {
			t.Fatalf("query=%q ok=%v", query, ok)
		}
	})
	t.Run("连续中文生成有界短词候选", func(t *testing.T) {
		query, ok := BuildFTSQuery("检索层怎么设计")
		if !ok {
			t.Fatal("expected query")
		}
		if strings.Contains(query, "检 索 层 怎 么 设 计") == false {
			t.Fatalf("missing full fragment: %q", query)
		}
		if strings.Count(query, `"`) > 2*maxQueryTerms {
			t.Fatalf("too many terms: %q", query)
		}
	})
	t.Run("引号短语必须命中", func(t *testing.T) {
		query, ok := BuildFTSQuery(`"记忆存储" SQLCipher`)
		if !ok {
			t.Fatal("expected query")
		}
		if !strings.HasPrefix(query, `"记 忆 存 储" AND (`) || !strings.Contains(query, `"SQLCipher"`) {
			t.Fatalf("structure: %q", query)
		}
	})
	t.Run("引号转义让用户无法控制 MATCH 语法", func(t *testing.T) {
		query, ok := BuildFTSQuery(`注入" OR NEAR(a b) OR "x`)
		if !ok {
			t.Fatal("expected query")
		}
		// 结构校验：剥掉转义后的字面短语，剩余部分只允许固定的 AND/OR/括号结构，
		// 用户输入无法逃出短语去控制 NEAR/NOT 等 MATCH 语法。
		rest := phrasePattern.ReplaceAllString(query, " ")
		rest = strings.NewReplacer("(", " ", ")", " ").Replace(rest)
		for _, token := range strings.Fields(rest) {
			if token != "AND" && token != "OR" {
				t.Fatalf("unexpected operator %q in %q", token, query)
			}
		}
	})
	t.Run("标识符与错误码保持整体", func(t *testing.T) {
		query, ok := BuildFTSQuery("SQLSTATE[23505] context_messages")
		if !ok {
			t.Fatal("expected query")
		}
		if !strings.Contains(query, `"SQLSTATE"`) || !strings.Contains(query, `"context_messages"`) {
			t.Fatalf("identifiers: %q", query)
		}
	})
	t.Run("超长查询被限制", func(t *testing.T) {
		raw := strings.Repeat("很长的中文查询内容", 200)
		query, ok := BuildFTSQuery(raw)
		if !ok {
			t.Fatal("expected query")
		}
		if strings.Count(query, `"`) > 2*maxQueryTerms {
			t.Fatalf("too many terms: %q", query)
		}
	})
}
