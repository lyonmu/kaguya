package agent

import (
	"fmt"
	"net/http"
	"net/url"
)

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
	return &providerHTTPClient{endpoint: *u, client: http.DefaultClient}, nil
}

func (c *providerHTTPClient) Do(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	endpoint := c.endpoint
	request.URL = &endpoint
	request.Host = endpoint.Host
	return c.client.Do(request)
}
