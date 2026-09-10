package mcp

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Config 是持久化连接配置；Enabled 单独管理，避免编辑配置意外切换状态。
type Config struct {
	Name             string            `json:"name" binding:"required,max=100"`
	Transport        string            `json:"transport" binding:"required,oneof=stdio streamable-http sse"`
	Command          string            `json:"command"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	WorkingDirectory string            `json:"working_directory"`
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers"`
	TimeoutSeconds   int               `json:"timeout_seconds" binding:"required,min=1,max=600"`
}

func (c Config) Timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }

func (c *Config) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len([]rune(c.Name)) > 100 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 600 {
		return fmt.Errorf("名称不能为空且最多 100 字，超时须为 1–600 秒")
	}
	for k, v := range c.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("环境变量名称或值无效")
		}
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("参数不能包含 NUL 字符")
		}
	}
	switch c.Transport {
	case "stdio":
		if strings.TrimSpace(c.Command) == "" || strings.ContainsRune(c.Command, 0) {
			return fmt.Errorf("stdio 必须提供有效的可执行文件")
		}
		if c.WorkingDirectory != "" && (!filepath.IsAbs(c.WorkingDirectory) || strings.ContainsRune(c.WorkingDirectory, 0)) {
			return fmt.Errorf("工作目录必须是绝对路径")
		}
		if c.URL != "" || len(c.Headers) > 0 {
			return fmt.Errorf("stdio 不支持 URL 或 HTTP 请求头")
		}
	case "streamable-http", "sse":
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("必须提供不含用户信息和片段的完整 HTTP(S) URL")
		}
		if c.Command != "" || len(c.Args) > 0 || len(c.Env) > 0 || c.WorkingDirectory != "" {
			return fmt.Errorf("HTTP 传输不支持命令、参数或环境变量")
		}
		seen := map[string]bool{}
		for k, v := range c.Headers {
			canonical := http.CanonicalHeaderKey(k)
			if k == "" || strings.IndexFunc(k, func(r rune) bool {
				return !strings.ContainsRune("!#$%&'*+-.^_`|~", r) && !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z')
			}) >= 0 || strings.IndexFunc(v, func(r rune) bool { return r == '\r' || r == '\n' || r == 0 || (unicode.IsControl(r) && r != '\t') }) >= 0 {
				return fmt.Errorf("HTTP 请求头格式无效")
			}
			if seen[canonical] {
				return fmt.Errorf("HTTP 请求头名称不能重复")
			}
			seen[canonical] = true
			switch canonical {
			case "Host", "Content-Length", "Connection", "Transfer-Encoding", "Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version":
				return fmt.Errorf("不能覆盖 MCP 协议请求头 %s", canonical)
			}
		}
	default:
		return fmt.Errorf("不支持的 MCP 传输类型")
	}
	return nil
}
