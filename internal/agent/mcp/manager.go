package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
	"github.com/kaptinlin/jsonschema"
	"github.com/lyonmu/kaguya/internal/global"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
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
	stopRoot        func() bool
	mu              sync.Mutex
	closing         bool
	active          sync.WaitGroup
	tools           []*sdk.Tool
	preparedTools   []preparedTool
	timeout         time.Duration
	generation      int64
}

// connectionSeq 在进程内为每条已发布连接分配单调 generation，供审批身份绑定；
// 同名工具在不同连接上不能被当成同一可信工具。
var connectionSeq atomic.Int64

type preparedTool struct {
	remoteName string
	info       fantasy.ToolInfo
	definition []byte
	validator  *jsonschema.Schema
}

// Prepare 是旧调用方的兼容入口；生产连接必须显式提供运行时生命周期。
func Prepare(ctx context.Context, config Config) (*Connection, error) {
	return PrepareWithLifetime(ctx, context.Background(), config)
}

// PrepareWithLifetime 的初始化受请求与最多 15 秒预算控制；连接成功后只受
// lifetimeCtx 控制，不能因 HTTP 请求返回而取消 stdio、SSE 或工具调用。
func PrepareWithLifetime(ctx, lifetimeCtx context.Context, config Config) (*Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, newError("MCP 请求已取消", "connect", err)
	}
	if err := lifetimeCtx.Err(); err != nil {
		return nil, newError("MCP 应用正在关闭", "connect", err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// root 立即停止工具调用；底层传输由 Close 在调用发送取消通知后关闭。
	// 直接让 transport 继承 root 会抢先切断 notifications/cancelled。
	life, cancel := context.WithCancel(context.WithoutCancel(lifetimeCtx))
	runCtx, cancelRun := context.WithCancel(lifetimeCtx)
	connection := &Connection{ctx: runCtx, cancel: cancelRun, transportCancel: cancel, timeout: config.Timeout(), generation: connectionSeq.Add(1)}
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
		client := &http.Client{Transport: headerTransport{headers: config.Headers, origin: endpoint, lifetime: life}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if config.Transport == "sse" {
			transport = &sdk.SSEClientTransport{Endpoint: config.URL, HTTPClient: client}
		} else {
			transport = &sdk.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client, MaxRetries: -1}
		}
	}
	connectCtx, stop := context.WithTimeout(ctx, min(config.Timeout(), 15*time.Second))
	defer stop()
	stopLifetime := context.AfterFunc(life, stop)
	defer stopLifetime()
	// 初始化阶段仍须响应 root；成功后由下面注册的 Close 接管清理顺序。
	stopRootConnect := context.AfterFunc(lifetimeCtx, stop)
	defer stopRootConnect()
	// SSE 的底层 GET 必须绑定连接生命周期，初始化超时只负责失败时取消它。
	stopConnect := context.AfterFunc(connectCtx, cancel)
	defer stopConnect()
	// SDK 日志可能包含远端内容，禁止将其写入应用日志。
	client := sdk.NewClient(&sdk.Implementation{Name: "kaguya", Version: "0.0.1"}, &sdk.ClientOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), KeepAlive: 30 * time.Second})
	var err error
	connection.session, err = client.Connect(connectCtx, persistentTransport{Transport: transport, ctx: life}, nil)
	if err != nil {
		cause := errors.Join(err, connectCtx.Err(), ctx.Err(), lifetimeCtx.Err())
		cancel()
		// 可执行文件缺失是最常见的 GUI 启动问题；给出可操作提示，其余错误保持通用。
		if errors.Is(err, exec.ErrNotFound) {
			return nil, newError(fmt.Sprintf("找不到 MCP 可执行文件 %q，请检查 PATH 或改用绝对路径", config.Command), "connect", cause)
		}
		return nil, newError("MCP 连接失败，请检查服务地址、命令及认证配置", "connect", cause)
	}
	for tool, err := range connection.session.Tools(connectCtx, nil) {
		if err != nil {
			cause := errors.Join(err, connectCtx.Err(), ctx.Err(), lifetimeCtx.Err())
			connection.Close()
			return nil, newError("MCP 工具发现失败", "discover", cause)
		}
		if len(connection.tools) >= 256 {
			connection.Close()
			cause := fmt.Errorf("tool limit exceeded")
			return nil, newProtocolError("MCP 工具数量超过 256", "discover", cause)
		}
		if _, err := toolInfo("", tool); err != nil {
			connection.Close()
			return nil, newProtocolError(err.Error(), "discover", err)
		}
		connection.tools = append(connection.tools, tool)
		if prepared, err := prepareTool(tool); err == nil {
			connection.preparedTools = append(connection.preparedTools, prepared)
		} else if global.Logger != nil {
			global.Logger.Warn("skip MCP tool with uncompiled schema",
				zap.String("stage", "compile_schema"),
				zap.Int("tool_index", len(connection.tools)-1))
		}
	}
	if !stopConnect() || connectCtx.Err() != nil || life.Err() != nil {
		cause := errors.Join(connectCtx.Err(), life.Err())
		connection.Close()
		return nil, newError("MCP 连接超时或请求已取消", "connect", cause)
	}
	// 部分 SDK transport 在取消底层 ctx 后不会自行终结 session.Wait；
	// root 仍须显式关闭 session。注册与 Close 读取同受 mu 保护。
	connection.mu.Lock()
	connection.stopRoot = context.AfterFunc(lifetimeCtx, connection.Close)
	connection.mu.Unlock()
	go func() { _ = connection.session.Wait(); connection.Close() }()
	return connection, nil
}

// Err 返回连接生命周期的取消状态，供发布前重新验证。
func (c *Connection) Err() error { return c.ctx.Err() }

func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		if c.stopRoot != nil {
			c.stopRoot()
		}
		c.cancel()
		c.mu.Unlock()
		// 先让调用发送 MCP cancelled 通知，再关闭会话，避免通知被关闭竞态丢弃。
		drained := make(chan struct{})
		go func() { c.active.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
		if c.session != nil {
			// 传输仍可用时让 SDK 完成 DELETE 等会话清理；无响应时仍有界
			// 取消底层请求，不能让优雅关闭越过应用生命周期。
			forceClose := time.AfterFunc(2*time.Second, c.transportCancel)
			_ = c.session.Close()
			forceClose.Stop()
		}
		c.transportCancel()
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
			status.Message = "MCP 连接已断开，正在自动重连"
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
	result := make([]fantasy.AgentTool, 0, len(c.preparedTools))
	for _, prepared := range c.preparedTools {
		result = append(result, newAgentToolFromPrepared(id, c, prepared))
	}
	return result
}

type persistentTransport struct {
	sdk.Transport
	ctx context.Context
}

func (t persistentTransport) Connect(context.Context) (sdk.Connection, error) {
	connection, err := t.Transport.Connect(t.ctx)
	if err != nil {
		return nil, err
	}
	// SDK 初始化失败可能没有返回 session；直接绑定实际 transport，保证
	// 所有失败路径也释放其 detached context，不包裹/隐藏 SDK 私有接口。
	context.AfterFunc(t.ctx, func() { _ = connection.Close() })
	return connection, nil
}

type headerTransport struct {
	headers  map[string]string
	origin   *url.URL
	lifetime context.Context
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Legacy SSE can advertise an absolute POST endpoint. Do not forward
	// credentials or tool arguments to a different origin (including HTTPS downgrade).
	if t.origin == nil || req.URL.User != nil || !strings.EqualFold(req.URL.Scheme, t.origin.Scheme) || !strings.EqualFold(req.URL.Host, t.origin.Host) {
		return nil, fmt.Errorf("MCP request endpoint must match the configured origin")
	}
	ctx := req.Context()
	var cancel context.CancelFunc
	var stop func() bool
	// SDK 的 detached HTTP 请求也必须受连接 root 控制；否则初始化取消通知
	// 及后台 GET 可越过本实例生命周期。DELETE 保留既有 2 秒优雅清理预算。
	if req.Method == http.MethodDelete {
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
	} else if t.lifetime != nil {
		ctx, cancel = context.WithCancel(ctx)
		stop = context.AfterFunc(t.lifetime, cancel)
		if t.lifetime.Err() != nil {
			cancel()
		}
	}
	cleanup := func() {
		if stop != nil {
			stop()
		}
		if cancel != nil {
			cancel()
		}
	}
	copy := req.Clone(ctx)
	for k, v := range t.headers {
		copy.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(copy)
	if err != nil {
		cleanup()
		return nil, err
	}
	if cancel != nil {
		resp.Body = &mcpHTTPBody{ReadCloser: resp.Body, cleanup: cleanup}
	}
	return resp, nil
}

// 请求所有权保持到 body 关闭或 EOF，不能在 RoundTrip 返回头时取消 SSE body。
type mcpHTTPBody struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (b *mcpHTTPBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.cleanup)
	}
	return n, err
}
func (b *mcpHTTPBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cleanup)
	return err
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
	return fantasy.ToolInfo{Name: toolName(id, tool.Name), Description: tool.Description, Parameters: properties, Required: required}, nil
}

func toolName(id, remoteName string) string {
	hash := sha256.Sum256([]byte(id + "\x00" + remoteName))
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, remoteName)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("mcp_%s_%x", name, hash[:12])
}

// prepareTool 在连接发现阶段编译完整的本地输入校验器。校验只使用远端声明的
// 真实约束；编译失败时保持原有语义，只从可调用适配器中跳过该工具。
func prepareTool(tool *sdk.Tool) (preparedTool, error) {
	info, err := toolInfo("", tool)
	if err != nil {
		return preparedTool{}, err
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return preparedTool{}, fmt.Errorf("encode MCP tool schema: %w", err)
	}
	validator, err := jsonschema.NewCompiler().Compile(raw)
	if err != nil {
		return preparedTool{}, fmt.Errorf("compile MCP tool schema: %w", err)
	}
	return preparedTool{remoteName: tool.Name, info: info, definition: raw, validator: validator}, nil
}

func newAgentToolFromPrepared(id string, connection *Connection, prepared preparedTool) *agentTool {
	info := prepared.info
	info.Name = toolName(id, prepared.remoteName)
	schemaHash := sha256.Sum256(prepared.definition)
	identity := fmt.Sprintf("mcp:%s:%s:%d:%s", id, prepared.remoteName, connection.generation, hex.EncodeToString(schemaHash[:8]))
	return &agentTool{connection: connection, remoteName: prepared.remoteName, info: info, definition: prepared.definition, validator: prepared.validator, identity: identity}
}

func newAgentTool(id string, connection *Connection, tool *sdk.Tool) (*agentTool, error) {
	prepared, err := prepareTool(tool)
	if err != nil {
		return nil, err
	}
	return newAgentToolFromPrepared(id, connection, prepared), nil
}

type agentTool struct {
	connection *Connection
	remoteName string
	identity   string
	info       fantasy.ToolInfo
	options    fantasy.ProviderOptions
	definition []byte
	validator  *jsonschema.Schema
}

// Info 返回独立的 schema 副本：Provider 可能原地规范化嵌套 schema，
// 不能把同一棵可变树交给后续请求复用。
func (t *agentTool) Info() fantasy.ToolInfo {
	info := t.info
	info.Required = slices.Clone(t.info.Required)
	info.Parameters = map[string]any{}
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

// PermissionIdentity 返回审批分类使用的可信身份；MCP 工具始终按 unknown 处理，
// 远端只读 hint 不单独免审批。
func (t *agentTool) PermissionIdentity() string { return t.identity }

// ValidateInput 只复用发现时编译的 schema，不登记 active call 或访问远端连接。
func (t *agentTool) ValidateInput(call fantasy.ToolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil || args == nil {
		return "MCP 工具参数必须为 JSON 对象"
	}
	return t.validateInput(args)
}

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
	if err := ctx.Err(); err != nil {
		return fantasy.ToolResponse{}, err
	}
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
