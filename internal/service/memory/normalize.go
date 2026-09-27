package memory

import (
	"strings"
	"unicode"
)

// NormalizerVersion 是搜索投影文本的规范化算法版本。
// 升级后需要从页面重新生成 SearchDoc 并重建索引，不能只执行 FTS rebuild。
const NormalizerVersion = 1

const (
	// maxQueryTerms 限制一次检索的检索词数量。
	maxQueryTerms = 64
	// maxQueryRunes 限制原始查询长度，超长直接截断后再分析。
	maxQueryRunes = 400
)

// lowInformationFragments 过滤中文短词候选里的常见低信息片段；
// 它是启发式词表，不是中文分词器。
var lowInformationFragments = map[string]bool{
	"的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
	"我": true, "你": true, "他": true, "它": true, "们": true, "这": true, "那": true,
	"有": true, "就": true, "不": true, "也": true, "都": true, "很": true, "吧": true, "吗": true,
	"什么": true, "怎么": true, "怎样": true, "如何": true, "可以": true, "还是": true,
	"这个": true, "那个": true, "一个": true, "一下": true, "现在": true, "时候": true,
	"请问": true, "帮我": true, "告诉": true, "为什么": true, "是不是": true, "能不能": true,
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana,
		unicode.Katakana, unicode.Hangul)
}

// NormalizeFTS 把 CJK 字符间隔开，使 unicode61 的 token 流包含单字 token，
// 从而两字中文词可以按短语查询命中；英文、数字与标点保持原样，
// 不能删除标点后把本来分开的 token 拼成不存在的新词。
func NormalizeFTS(text string) string {
	var b strings.Builder
	for _, r := range text {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// LiteralFTSPhrase 把规范化后的检索词转义为 FTS5 字面短语；
// 调用方不能把模型或用户输入直接交给 MATCH 语法。
func LiteralFTSPhrase(term string) string {
	s := NormalizeFTS(strings.TrimSpace(term))
	if s == "" {
		return ""
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// BuildFTSQuery 从自然语言查询构造固定结构的 FTS5 表达式：
// 引号内容作为必须命中的短语，其余检索词（ASCII 标识/路径/错误码与中文短词候选）
// 以 OR 组合。没有有效检索词时返回 ok=false，调用方必须返回空召回，
// 不能构造一个扫描所有页面的隐式查询。返回值只能作为参数传给 MATCH。
func BuildFTSQuery(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if runes := []rune(raw); len(runes) > maxQueryRunes {
		raw = string(runes[:maxQueryRunes])
	}
	var phrases, terms []string
	seen := map[string]bool{}
	add := func(list *[]string, term string) {
		if len(*list) >= maxQueryTerms {
			return
		}
		phrase := LiteralFTSPhrase(term)
		if phrase == "" || seen[phrase] {
			return
		}
		// 长度为 1 的纯 ASCII 单字符几乎没有区分度。
		if utf8Len(term) < 2 && isASCIIStr(term) {
			return
		}
		seen[phrase] = true
		*list = append(*list, phrase)
	}
	chunks := splitQuery(raw)
	// 标识符与明确引用优先，避免前面的中文问句耗尽检索词预算。
	for _, chunk := range chunks {
		switch chunk.kind {
		case chunkQuoted:
			add(&phrases, chunk.text)
		case chunkASCII:
			add(&terms, chunk.text)
		}
	}
	for _, chunk := range chunks {
		if chunk.kind == chunkCJK {
			for _, candidate := range cjkCandidates(chunk.text) {
				add(&terms, candidate)
			}
		}
	}
	if len(phrases)+len(terms) == 0 {
		return "", false
	}
	// 引号短语必须全部命中；其余检索词任一命中即可参与排序。
	group := terms
	if len(group) == 0 {
		return strings.Join(phrases, " AND "), true
	}
	or := strings.Join(group, " OR ")
	switch {
	case len(phrases) == 0:
		return or, true
	case len(or) == 0:
		return strings.Join(phrases, " AND "), true
	default:
		return strings.Join(phrases, " AND ") + " AND (" + or + ")", true
	}
}

type chunkKind int

const (
	chunkQuoted chunkKind = iota
	chunkASCII
	chunkCJK
)

type queryChunk struct {
	kind chunkKind
	text string
}

// splitQuery 提取引号内容、ASCII 标识/路径/错误码与连续 CJK 片段；
// 其余标点作为分隔，不参与拼接。
func splitQuery(raw string) []queryChunk {
	var chunks []queryChunk
	runes := []rune(raw)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == '"' || r == '“':
			closer := '"'
			if r == '“' {
				closer = '”'
			}
			j := i + 1
			for j < len(runes) && runes[j] != closer {
				j++
			}
			if text := strings.TrimSpace(string(runes[i+1 : j])); text != "" {
				chunks = append(chunks, queryChunk{chunkQuoted, text})
			}
			i = min(j+1, len(runes))
		case isASCII(r) && (isAlnum(r) || r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '#'):
			j := i
			for j < len(runes) && isASCII(runes[j]) && (isAlnum(runes[j]) || runes[j] == '_' || runes[j] == '-' || runes[j] == '.' || runes[j] == '/' || runes[j] == ':' || runes[j] == '#') {
				j++
			}
			chunks = append(chunks, queryChunk{chunkASCII, string(runes[i:j])})
			i = j
		case isCJK(r):
			j := i
			for j < len(runes) && isCJK(runes[j]) {
				j++
			}
			chunks = append(chunks, queryChunk{chunkCJK, string(runes[i:j])})
			i = j
		default:
			i++
		}
	}
	return chunks
}

// cjkCandidates 保留短主题原词，再按两字片段检索。四字窗口优先会让
// “怎么加密”匹配不到“加密”，长问句也会在走到两字词前耗尽预算。
// 仅查询算法变化，索引中的单字 token 不需要迁移。
func cjkCandidates(text string) []string {
	runes := []rune(text)
	if len(runes) < 2 || lowInformationFragments[text] {
		return nil
	}
	var out []string
	if len(runes) <= 8 {
		out = append(out, text)
	}
	for start := 0; start+2 <= len(runes); start++ {
		piece := string(runes[start : start+2])
		if !lowInformationFragments[piece] {
			out = append(out, piece)
		}
	}
	return out
}

func isASCII(r rune) bool { return r <= 0x7F }

func isAlnum(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func isASCIIStr(s string) bool {
	for _, r := range s {
		if !isASCII(r) {
			return false
		}
	}
	return true
}

func utf8Len(s string) int { return len([]rune(s)) }
