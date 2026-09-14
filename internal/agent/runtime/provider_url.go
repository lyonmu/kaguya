package agent

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
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

// providerHTTPClient 使用配置的完整请求 URL，禁止 SDK 追加或修改端点路径。
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
