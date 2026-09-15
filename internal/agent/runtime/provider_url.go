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

// protocolSDKPath 是各协议 SDK 在 BaseURL 后面自行追加的相对路径。
var protocolSDKPath = map[consts.ProviderProtocol]string{
	consts.ProtocolOpenAIChat:      "chat/completions",
	consts.ProtocolOpenAIResponses: "responses",
	consts.ProtocolAnthropic:       "v1/messages",
}

// providerRequest 描述一次模型请求的寻址：最终地址始终是提供商 BaseURL 与模型请求路径的拼接。
// 路径以协议端点结尾时，反推 SDK 需要的 BaseURL，让 SDK 自己拼出真实地址（报错里的地址也是真实地址）；
// 否则保留 SDK 的 BaseURL，由 HTTP 客户端在发送前改写为拼接后的地址。
type providerRequest struct {
	sdkBaseURL string
	endpoint   *url.URL // 非空时覆盖 SDK 拼出的请求地址
}

func newProviderRequest(baseURL, requestPath string, protocol consts.ProviderProtocol) (*providerRequest, error) {
	sdkPath, ok := protocolSDKPath[protocol]
	if !ok {
		return nil, fmt.Errorf("unsupported provider protocol %q", protocol)
	}
	base, path := strings.TrimRight(baseURL, "/"), strings.TrimLeft(requestPath, "/")
	if base == "" || path == "" {
		return nil, errors.New("provider base URL and model request path are required")
	}
	final := base + "/" + path
	u, err := url.Parse(final)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("provider request URL must be a complete HTTP(S) URL without userinfo, query or fragment")
	}
	if strings.HasSuffix(final, "/"+sdkPath) {
		// 去掉 SDK 自己会追加的相对路径，保留结尾的 /，让相对路径解析出同一最终地址。
		return &providerRequest{sdkBaseURL: strings.TrimSuffix(final, sdkPath)}, nil
	}
	return &providerRequest{sdkBaseURL: baseURL, endpoint: u}, nil
}

// providerHTTPClient 只负责传输：共享连接池、响应头超时，并抑制 SDK 自动追加的 User-Agent。
// endpoint 非空时用它覆盖 SDK 自己拼出的请求地址，适用于路径不以协议端点结尾的配置。
type providerHTTPClient struct {
	client   *http.Client
	endpoint *url.URL
}

func newProviderHTTPClient(endpoint *url.URL) *providerHTTPClient {
	return &providerHTTPClient{client: sharedProviderHTTPClient, endpoint: endpoint}
}

func (c *providerHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if c.endpoint != nil {
		endpoint := *c.endpoint
		req.URL = &endpoint
		req.Host = endpoint.Host
	}
	req.Header["User-Agent"] = nil
	return c.client.Do(req)
}
