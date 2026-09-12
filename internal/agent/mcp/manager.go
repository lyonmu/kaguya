package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/kaptinlin/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var Default = NewManager()

type Status struct {
	State   string   `json:"state"`
	Message string   `json:"message,omitempty"`
	Tools   []string `json:"tools"`
}

type Manager struct {
	mu          sync.RWMutex
	connections map[string]*Connection
	failures    map[string]string
}

func NewManager() *Manager {
	return &Manager{connections: map[string]*Connection{}, failures: map[string]string{}}
}

type Connection struct {
	session         *sdk.ClientSession
	ctx             context.Context
	cancel          context.CancelFunc
	transportCancel context.CancelFunc
	closeOnce       sync.Once
	mu              sync.Mutex
	closing         bool
	active          sync.WaitGroup
	tools           []*sdk.Tool
	timeout         time.Duration
}

// Prepare 先完成连接和工具发现，只有数据库保存成功后才发布连接。
func Prepare(ctx context.Context, config Config) (*Connection, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	runCtx, cancelRun := context.WithCancel(life)
	connection := &Connection{ctx: runCtx, cancel: cancelRun, transportCancel: cancel, timeout: config.Timeout()}
	var transport sdk.Transport
	switch config.Transport {
	case "stdio":
		cmd := exec.CommandContext(life, config.Command, config.Args...)
		cmd.Dir = config.WorkingDirectory
		cmd.Env = os.Environ()
		keys := make([]string, 0, len(config.Env))
		for key := range config.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmd.Env = append(cmd.Env, key+"="+config.Env[key])
		}
		configureProcess(cmd)
		cmd.WaitDelay = time.Second
		transport = &sdk.CommandTransport{Command: cmd, TerminateDuration: time.Second}
	default:
		endpoint, _ := url.Parse(config.URL) // Config.Validate has checked the URL.
		client := &http.Client{Transport: headerTransport{headers: config.Headers, origin: endpoint}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if config.Transport == "sse" {
			transport = &sdk.SSEClientTransport{Endpoint: config.URL, HTTPClient: client}
		} else {
			transport = &sdk.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client, MaxRetries: -1}
		}
	}
	connectCtx, stop := context.WithTimeout(ctx, min(config.Timeout(), 15*time.Second))
	defer stop()
	// SSE 的底层 GET 必须绑定连接生命周期，初始化超时只负责失败时取消它。
	stopConnect := context.AfterFunc(connectCtx, cancel)
	defer stopConnect()
	// SDK 日志可能包含远端内容，禁止将其写入应用日志。
	client := sdk.NewClient(&sdk.Implementation{Name: "kaguya", Version: "0.0.1"}, &sdk.ClientOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), KeepAlive: 30 * time.Second})
	var err error
	connection.session, err = client.Connect(connectCtx, persistentTransport{Transport: transport, ctx: life}, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("MCP 连接失败，请检查服务地址、命令及认证配置")
	}
	for tool, err := range connection.session.Tools(connectCtx, nil) {
		if err != nil {
			connection.Close()
			return nil, fmt.Errorf("MCP 工具发现失败")
		}
		if len(connection.tools) >= 256 {
			connection.Close()
			return nil, fmt.Errorf("MCP 工具数量超过 256")
		}
		if _, err := toolInfo("", tool); err != nil {
			connection.Close()
			return nil, err
		}
		connection.tools = append(connection.tools, tool)
	}
	if !stopConnect() || connectCtx.Err() != nil {
		connection.Close()
		return nil, fmt.Errorf("MCP 连接超时或请求已取消")
	}
	go func() { _ = connection.session.Wait(); connection.cancel(); connection.transportCancel() }()
	return connection, nil
}

func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.cancel()
		c.mu.Unlock()
		// 先让调用发送 MCP cancelled 通知，再关闭会话，避免通知被关闭竞态丢弃。
		drained := make(chan struct{})
		go func() { c.active.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
		c.transportCancel()
		if c.session != nil {
			_ = c.session.Close()
		}
	})
}

func (m *Manager) Replace(id string, next *Connection) {
	m.mu.Lock()
	old := m.connections[id]
	delete(m.failures, id)
	if next == nil {
		delete(m.connections, id)
	} else {
		m.connections[id] = next
	}
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
}
func (m *Manager) Failed(id, message string) { m.mu.Lock(); m.failures[id] = message; m.mu.Unlock() }
func (m *Manager) Status(id string) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := Status{State: "stopped", Tools: []string{}}
	if message := m.failures[id]; message != "" {
		status.State = "error"
		status.Message = message
	}
	if c := m.connections[id]; c != nil {
		status.State = "running"
		if c.ctx.Err() != nil {
			status.State = "error"
			status.Message = "MCP 连接已断开，请重新启用"
			return status
		}
		for _, tool := range c.tools {
			status.Tools = append(status.Tools, tool.Name)
		}
	}
	return status
}
func (m *Manager) Close() {
	m.mu.Lock()
	connections := m.connections
	m.connections = map[string]*Connection{}
	m.failures = map[string]string{}
	m.mu.Unlock()
	for _, c := range connections {
		c.Close()
	}
}

// Tools 为每轮对话创建独立适配器，旧轮次中的适配器随原连接停用而失效。
func (m *Manager) Tools() []fantasy.AgentTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := []fantasy.AgentTool{}
	ids := make([]string, 0, len(m.connections))
	for id := range m.connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := m.connections[id]
		if c.ctx.Err() != nil {
			continue
		}
		result = append(result, buildTools(id, c)...)
	}
	return result
}

// buildTools 为一条连接构造全部工具适配器，schema 无法本地校验时跳过该工具。
func buildTools(id string, c *Connection) []fantasy.AgentTool {
	result := make([]fantasy.AgentTool, 0, len(c.tools))
	for _, t := range c.tools {
		tool, err := newAgentTool(id, c, t)
		if err != nil {
			continue
		}
		result = append(result, tool)
	}
	return result
}

type persistentTransport struct {
	sdk.Transport
	ctx context.Context
}

func (t persistentTransport) Connect(context.Context) (sdk.Connection, error) {
	return t.Transport.Connect(t.ctx)
}

type headerTransport struct {
	headers map[string]string
	origin  *url.URL
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Legacy SSE can advertise an absolute POST endpoint. Do not forward
	// credentials or tool arguments to a different origin (including HTTPS downgrade).
	if t.origin == nil || req.URL.User != nil || !strings.EqualFold(req.URL.Scheme, t.origin.Scheme) || !strings.EqualFold(req.URL.Host, t.origin.Host) {
		return nil, fmt.Errorf("MCP request endpoint must match the configured origin")
	}
	ctx := req.Context()
	// 关闭会话的 DELETE 使用独立上限，避免故障远端阻塞动态停用。
	if req.Method == http.MethodDelete {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	copy := req.Clone(ctx)
	for k, v := range t.headers {
		copy.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(copy)
}

func toolInfo(id string, tool *sdk.Tool) (fantasy.ToolInfo, error) {
	var schema map[string]any
	raw, err := json.Marshal(tool.InputSchema)
	if err == nil {
		err = json.Unmarshal(raw, &schema)
	}
	if err != nil || schema == nil || schema["type"] != "object" {
		return fantasy.ToolInfo{}, fmt.Errorf("MCP 工具参数必须为 object schema")
	}
	// Fantasy 只支持顶层 properties/required；拒绝无法保真转换的根约束。
	// additionalProperties/min/maxProperties 在 SDK 生成的闭集 schema 中很常见，
	// 已声明字段仍会完整保留，因此不拒绝；仅限制模型可见的字段范围。
	for _, key := range []string{"$ref", "$defs", "definitions", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "patternProperties", "dependentSchemas", "dependentRequired"} {
		if _, ok := schema[key]; ok {
			return fantasy.ToolInfo{}, fmt.Errorf("MCP 工具 schema 包含不支持的根关键字 %s", key)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	required := []string{}
	if values, ok := schema["required"].([]any); ok {
		for _, v := range values {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
	}
	hash := sha256.Sum256([]byte(id + "\x00" + tool.Name))
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, tool.Name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fantasy.ToolInfo{Name: fmt.Sprintf("mcp_%s_%x", name, hash[:12]), Description: tool.Description, Parameters: properties, Required: required}, nil
}

// newAgentTool 构造可调用工具，并编译完整的本地输入校验器。
// 校验只使用远端声明的真实约束，不自行猜测参数；校验失败不触达远端。
func newAgentTool(id string, connection *Connection, tool *sdk.Tool) (*agentTool, error) {
	info, err := toolInfo(id, tool)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool schema: %w", err)
	}
	validator, err := jsonschema.NewCompiler().Compile(raw)
	if err != nil {
		return nil, fmt.Errorf("compile MCP tool schema: %w", err)
	}
	return &agentTool{connection: connection, remoteName: tool.Name, info: info, definition: raw, validator: validator}, nil
}

type agentTool struct {
	connection *Connection
	remoteName string
	info       fantasy.ToolInfo
	options    fantasy.ProviderOptions
	definition []byte
	validator  *jsonschema.Schema
}

// Info 返回独立的 schema 副本：Provider 可能原地规范化嵌套 schema，
// 不能把同一棵可变树交给后续请求复用。
func (t *agentTool) Info() fantasy.ToolInfo {
	info := t.info
	var definition map[string]any
	if err := json.Unmarshal(t.definition, &definition); err == nil {
		if properties, ok := definition["properties"].(map[string]any); ok {
			info.Parameters = properties
		}
	}
	return info
}

func (t *agentTool) ProviderOptions() fantasy.ProviderOptions           { return t.options }
func (t *agentTool) SetProviderOptions(options fantasy.ProviderOptions) { t.options = options }

// validateInput 按远端声明的 schema 本地校验参数，返回用户可读的工具错误。
func (t *agentTool) validateInput(args map[string]any) string {
	if t.validator == nil {
		return ""
	}
	result := t.validator.Validate(args)
	if result.IsValid() {
		return ""
	}
	details := make([]string, 0)
	for path, message := range result.DetailedErrors() {
		details = append(details, path+": "+message)
	}
	sort.Strings(details)
	return "invalid parameters for " + t.remoteName + ": " + strings.Join(details, "; ") + ". Correct these fields using the tool schema before retrying; no remote call was made."
}

func (t *agentTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	c := t.connection
	c.mu.Lock()
	if c.closing || c.ctx.Err() != nil {
		c.mu.Unlock()
		return fantasy.NewTextErrorResponse("MCP 服务已停用或连接已断开"), nil
	}
	c.active.Add(1)
	c.mu.Unlock()
	defer c.active.Done()
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil || args == nil {
		return fantasy.NewTextErrorResponse("MCP 工具参数必须为 JSON 对象"), nil
	}
	if message := t.validateInput(args); message != "" {
		return fantasy.NewTextErrorResponse(message), nil
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	result, err := c.session.CallTool(callCtx, &sdk.CallToolParams{Name: t.remoteName, Arguments: args})
	if err != nil {
		if ctx.Err() != nil {
			return fantasy.ToolResponse{}, ctx.Err()
		}
		return fantasy.NewTextErrorResponse("MCP 工具调用失败、超时或服务已停用"), nil
	}
	content, err := json.Marshal(result)
	if err != nil {
		return fantasy.NewTextErrorResponse("MCP 工具结果无法解析"), nil
	}
	if len(content) > 65536 {
		content = append(content[:65536], []byte("\n[MCP output truncated]")...)
	}
	response := fantasy.NewTextResponse(strings.ToValidUTF8(string(content), "�"))
	response.IsError = result.IsError
	return response, nil
}
