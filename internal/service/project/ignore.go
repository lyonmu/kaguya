package project

import (
	"bufio"
	"os"
	"path"
	"regexp"
	"strings"
)

// ignoreMatcher 按 .gitignore / .dockerignore 的常用语义过滤文件树：
// 注释、空行、! 取反、结尾 / 限定目录、前导或内嵌 / 锚定、*、?、[]、**。
type ignoreMatcher struct {
	root   *os.Root
	rules  []ignoreRule            // 根 .gitignore 与 .dockerignore，按文件顺序
	nested map[string][]ignoreRule // 子目录 .gitignore，首次访问时加载
}

type ignoreRule struct {
	pattern *regexp.Regexp
	negate  bool
	dirOnly bool
}

// newIgnoreMatcher 读取项目根的忽略文件；子目录 .gitignore 在遍历到对应路径时懒加载。
func newIgnoreMatcher(root *os.Root) *ignoreMatcher {
	matcher := &ignoreMatcher{root: root, nested: map[string][]ignoreRule{}}
	matcher.rules = append(matcher.rules, readIgnoreFile(root, ".gitignore", "")...)
	matcher.rules = append(matcher.rules, readIgnoreFile(root, ".dockerignore", "")...)
	return matcher
}

// ignored 判断项目根相对路径是否被忽略；目录被忽略时调用方应跳过整个子树。
func (m *ignoreMatcher) ignored(rel string, isDir bool) bool {
	if m == nil {
		return false
	}
	ignored := false
	apply := func(rules []ignoreRule) {
		for _, rule := range rules {
			if rule.match(rel, isDir) {
				ignored = !rule.negate
			}
		}
	}
	apply(m.rules)
	// 越深的 .gitignore 规则越晚应用，优先级越高。
	prefix := ""
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if prefix == "" {
			prefix = part
		} else {
			prefix += "/" + part
		}
		apply(m.rulesFor(prefix))
	}
	return ignored
}

func (m *ignoreMatcher) rulesFor(dir string) []ignoreRule {
	if rules, ok := m.nested[dir]; ok {
		return rules
	}
	rules := readIgnoreFile(m.root, path.Join(dir, ".gitignore"), dir)
	m.nested[dir] = rules
	return rules
}

// readIgnoreFile 读取并编译忽略文件；文件不存在或不可读时返回空规则。
func readIgnoreFile(root *os.Root, name, base string) []ignoreRule {
	file, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	var rules []ignoreRule
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		if rule, ok := parseIgnoreRule(scanner.Text(), base); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// parseIgnoreRule 编译单行规则；base 是该忽略文件所在目录的项目根相对路径。
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
