package workspace

import (
	"bufio"
	"context"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
)

// IgnoreMatcher 按 .gitignore / .dockerignore 的常用语义过滤工作区文件树：
// 注释、空行、! 取反、结尾 / 限定目录、前导或内嵌 / 锚定、*、?、[]、**。
//
// 子目录 .gitignore 在首次匹配到对应路径时懒加载；strict 模式下读取失败会返回
// 错误，调用方不得把它当作“没有忽略规则”继续放行内容。
type IgnoreMatcher struct {
	root   *os.Root
	strict bool
	ctx    context.Context

	mu     sync.Mutex
	rules  []ignoreRule            // 根 .gitignore 与 .dockerignore，按文件顺序
	nested map[string][]ignoreRule // 子目录 .gitignore，首次访问时加载
}

type ignoreRule struct {
	pattern *regexp.Regexp
	negate  bool
	dirOnly bool
}

// NewIgnoreMatcher 读取项目根的忽略文件；读取失败立即返回错误。
// 用于必须完整解释忽略规则的引用解析等场景。
func NewIgnoreMatcher(ctx context.Context, root *os.Root) (*IgnoreMatcher, error) {
	matcher := &IgnoreMatcher{root: root, ctx: ctx, strict: true, nested: map[string][]ignoreRule{}}
	for _, name := range []string{".gitignore", ".dockerignore"} {
		rules, err := readIgnoreFile(ctx, root, name, "")
		if err != nil {
			return nil, err
		}
		matcher.rules = append(matcher.rules, rules...)
	}
	return matcher, nil
}

// NewLenientIgnoreMatcher 复用同一匹配语义，但忽略文件不可读时按空规则处理，
// 保持现有 UI 文件树的默认行为。
func NewLenientIgnoreMatcher(ctx context.Context, root *os.Root) *IgnoreMatcher {
	matcher := &IgnoreMatcher{root: root, ctx: ctx, nested: map[string][]ignoreRule{}}
	for _, name := range []string{".gitignore", ".dockerignore"} {
		rules, _ := readIgnoreFile(ctx, root, name, "")
		matcher.rules = append(matcher.rules, rules...)
	}
	return matcher
}

// Ignored 是宽松入口：读取失败按“未忽略”处理；strict 模式应使用 Match。
func (m *IgnoreMatcher) Ignored(rel string, isDir bool) bool {
	ignored, err := m.Match(rel, isDir)
	if err != nil {
		return false
	}
	return ignored
}

// Match 判断工作区相对路径是否被忽略；目录被忽略时调用方应跳过整个子树。
func (m *IgnoreMatcher) Match(rel string, isDir bool) (bool, error) {
	if m == nil {
		return false, nil
	}
	if err := m.ctx.Err(); err != nil {
		return false, err
	}
	ignored := false
	apply := func(rules []ignoreRule) error {
		for _, rule := range rules {
			if err := m.ctx.Err(); err != nil {
				return err
			}
			if rule.match(rel, isDir) {
				ignored = !rule.negate
			}
		}
		return nil
	}
	if err := apply(m.rules); err != nil {
		return false, err
	}
	// 越深的 .gitignore 规则越晚应用，优先级越高。
	prefix := ""
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if err := m.ctx.Err(); err != nil {
			return false, err
		}
		if prefix == "" {
			prefix = part
		} else {
			prefix += "/" + part
		}
		rules, err := m.rulesFor(prefix)
		if err != nil {
			return false, err
		}
		if err := apply(rules); err != nil {
			return false, err
		}
	}
	return ignored, nil
}

func (m *IgnoreMatcher) rulesFor(dir string) ([]ignoreRule, error) {
	m.mu.Lock()
	if rules, ok := m.nested[dir]; ok {
		m.mu.Unlock()
		return rules, nil
	}
	m.mu.Unlock()
	rules, err := readIgnoreFile(m.ctx, m.root, path.Join(dir, ".gitignore"), dir)
	if err != nil && (m.strict || m.ctx.Err() != nil) {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.nested[dir]; ok {
		return previous, nil
	}
	m.nested[dir] = rules
	return rules, nil
}

// readIgnoreFile 读取并编译忽略文件；文件不存在返回空规则，其它错误原样返回。
func readIgnoreFile(ctx context.Context, root *os.Root, name, base string) ([]ignoreRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, _, err := OpenRegular(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var rules []ignoreRule
	scanner := bufio.NewScanner(ignoreReader{ctx: ctx, Reader: file})
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if rule, ok := parseIgnoreRule(scanner.Text(), base); ok {
			rules = append(rules, rule)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

// ignoreReader 在每次有界读取前检查取消；普通文件 open/stat 在 OpenRegular 处理。
type ignoreReader struct {
	ctx context.Context
	io.Reader
}

func (r ignoreReader) Read(buf []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(buf)
}

// parseIgnoreRule 编译单行规则；base 是该忽略文件所在目录的工作区相对路径。
func parseIgnoreRule(line, base string) (ignoreRule, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	line = trimTrailingSpaces(line)
	negate := strings.HasPrefix(line, "!")
	if negate {
		line = line[1:]
	}
	dirOnly := strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	if line == "" {
		return ignoreRule{}, false
	}
	// 含有 / 的模式锚定到忽略文件所在目录，否则匹配任意层级。
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	prefix := ""
	if base != "" {
		prefix = regexp.QuoteMeta(base) + "/"
	}
	expression := "^" + prefix
	if !anchored {
		expression += "(?:.*/)?"
	}
	expression += globRegexp(line) + "$"
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return ignoreRule{}, false
	}
	return ignoreRule{pattern: pattern, negate: negate, dirOnly: dirOnly}, true
}

func (r ignoreRule) match(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	return r.pattern.MatchString(rel)
}

// trimTrailingSpaces 去掉未用反斜杠转义的结尾空格。
func trimTrailingSpaces(line string) string {
	for len(line) > 0 && line[len(line)-1] == ' ' {
		backslashes := 0
		for index := len(line) - 2; index >= 0 && line[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

// globRegexp 把 gitignore 通配模式转成正则片段，** 独占路径段时才跨目录匹配。
func globRegexp(pattern string) string {
	var builder strings.Builder
	runes := []rune(pattern)
	for index := 0; index < len(runes); index++ {
		switch char := runes[index]; char {
		case '\\':
			if index+1 < len(runes) {
				index++
				builder.WriteString(regexp.QuoteMeta(string(runes[index])))
			}
		case '*':
			if index+1 < len(runes) && runes[index+1] == '*' {
				prevSlash := index == 0 || runes[index-1] == '/'
				next := index + 2
				nextSlash := next == len(runes) || runes[next] == '/'
				switch {
				case prevSlash && nextSlash && next < len(runes):
					builder.WriteString("(?:.*/)?")
					index = next
				case prevSlash && next == len(runes):
					builder.WriteString(".*")
					index++
				default:
					builder.WriteString("[^/]*")
					index++
				}
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := index + 1
			if end < len(runes) && (runes[end] == '!' || runes[end] == '^') {
				end++
			}
			if end < len(runes) && runes[end] == ']' {
				end++
			}
			for end < len(runes) && runes[end] != ']' {
				end++
			}
			if end >= len(runes) {
				builder.WriteString(regexp.QuoteMeta(string(char)))
				break
			}
			content := string(runes[index+1 : end])
			if strings.HasPrefix(content, "!") {
				content = "^" + content[1:]
			}
			builder.WriteString("[" + strings.ReplaceAll(content, "\\", "\\\\") + "]")
			index = end
		default:
			builder.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	return builder.String()
}
