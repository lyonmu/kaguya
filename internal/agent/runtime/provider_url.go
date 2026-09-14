package agent

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
)

// providerHeaderTimeout 限制连接建立后等待响应头的时间；流式正文的长时间静默
// 由 idleStreamModel 看门狗处理，两者互补。
const providerHeaderTimeout = 2 * time.Minute

// 所有提供商共享同一连接池，避免每轮对话新建 Transport 造成连接无法复用；
// 提高单主机空闲连接数，支持多个会话同时向同一提供商发起流式请求。
var sharedProviderHTTPClient = &http.Client{Transport: newProviderTransport()}

func newProviderTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = providerHeaderTimeout
	transport.MaxIdleConnsPerHost = 16
	return transport
}

// protocolEndpointSuffix 是各协议在提供商根地址后追加的固定端点路径。
var protocolEndpointSuffix = map[consts.ProviderProtocol]string{
	consts.ProtocolOpenAIChat:      "/chat/completions",
	consts.ProtocolOpenAIResponses: "/responses",
	consts.ProtocolAnthropic:       "/messages",
}

// providerEndpoint 把只到 API 版本段的提供商根地址补成最终请求地址，
// 端点路径由模型协议决定；根地址不能带查询或片段，未知协议直接报错。
func providerEndpoint(baseURL string, protocol consts.ProviderProtocol) (string, error) {
	suffix, ok := protocolEndpointSuffix[protocol]
	if !ok {
		return "", fmt.Errorf("unsupported provider protocol %q", protocol)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse provider base URL: %w", err)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("provider base URL must not contain query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	return u.String(), nil
}

// providerHTTPClient 使用根地址加协议端点后的最终请求 URL，禁止 SDK 追加或修改端点路径。
// SDK 仍负责请求体、认证、流式解析；这里仅指定最终请求地址。
type providerHTTPClient struct {
	endpoint url.URL
	client   *http.Client
}

func newProviderHTTPClient(raw string) (*providerHTTPClient, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("provider request URL must be a complete HTTP(S) URL without userinfo or fragment")
	}
	return &providerHTTPClient{endpoint: *u, client: sharedProviderHTTPClient}, nil
}

func (c *providerHTTPClient) Do(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	endpoint := c.endpoint
	request.URL = &endpoint
	request.Host = endpoint.Host
	// 不发送 SDK 或 net/http 自动生成的 User-Agent。
	request.Header["User-Agent"] = nil
	return c.client.Do(request)
}
