package agent

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lyonmu/kaguya/internal/consts"
)

// normalizeProviderBaseURL 将域名或完整端点转换为 SDK 需要的基础地址。
// 自定义基础路径保持不变；端点由 SDK 按协议追加，不修改持久化配置。
func normalizeProviderBaseURL(raw string, protocol consts.ProviderProtocol) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil // 使用 SDK 官方默认地址。
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("provider base URL must be an absolute HTTP(S) URL")
	}
	path := strings.TrimRight(u.Path, "/")
	switch protocol {
	case consts.ProtocolOpenAIChat, consts.ProtocolOpenAIResponses:
		endpoint := "/chat/completions"
		if protocol == consts.ProtocolOpenAIResponses {
			endpoint = "/responses"
		}
		path = strings.TrimSuffix(path, endpoint)
		if path == "" {
			path = "/v1"
		}
	case consts.ProtocolAnthropic:
		// Anthropic SDK 自行追加 v1/messages，而 OpenAI SDK 不追加 v1。
		path = strings.TrimSuffix(path, "/messages")
		path = strings.TrimSuffix(path, "/v1")
	}
	if path != strings.TrimRight(u.Path, "/") {
		u.Path = path
		u.RawPath = ""
	}
	return u.String(), nil
}
