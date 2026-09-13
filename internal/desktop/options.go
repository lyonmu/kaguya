// Package desktop 把既有的 Gin Handler 接到 Wails 原生 WebView 通道上。
// 它不注册业务 Binding：业务请求仍走原 REST/SSE 接口，只是底层传输换成了
// Wails 的原生 URL scheme。只有复制与打开外链两个窄接口调用宿主系统 API。
package desktop

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/lyonmu/kaguya/pkg"
)

// Options 是 Desktop 宿主的启动参数。
type Options struct {
	// Name/Description 用于应用菜单与关于窗口。
	Name        string
	Description string
	// WindowTitle 是窗口标题。
	WindowTitle string
	// Width/Height/MinWidth/MinHeight 是窗口初始尺寸与最小尺寸。
	Width, Height       int
	MinWidth, MinHeight int
	// APIPrefix 是业务路由前缀，作为非秘密配置通过 fragment 传给前端。
	APIPrefix string

	// RootContext 取消后所有经原生通道进入的请求都会被取消。
	RootContext context.Context
	// Admission 与 Web 模式共用同一套请求准入控制。
	Admission *pkg.Admission

	// Start 执行共享初始化并返回业务 Handler，只运行一次。
	Start func() (http.Handler, error)
	// Shutdown 在准入停止、在途请求排空后释放应用资源，只运行一次。
	Shutdown func()
}

// Validate 校验宿主参数；错误信息不包含任何秘密。
func (o Options) Validate() error {
	if o.Name == "" || o.WindowTitle == "" {
		return fmt.Errorf("desktop options require an application name and window title")
	}
	if err := validateAPIPrefix(o.APIPrefix); err != nil {
		return err
	}
	if o.RootContext == nil || o.Admission == nil {
		return fmt.Errorf("desktop options require a root context and admission gate")
	}
	if o.Start == nil || o.Shutdown == nil {
		return fmt.Errorf("desktop options require start and shutdown callbacks")
	}
	return nil
}

// reservedTopLevelPaths 是原生通道与静态资源占用的路径，业务前缀不能与它们重叠。
var reservedTopLevelPaths = []string{"/wails", nativeRouteRoot, "/assets", "/kaguya-favicon.webp"}

// validateAPIPrefix 要求业务前缀是没有 query、fragment 与越级段的本地绝对路径，
// 且不与原生保留路径冲突。
func validateAPIPrefix(prefix string) error {
	if prefix == "" || !strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("desktop API prefix must be an absolute local path")
	}
	if strings.ContainsAny(prefix, "?#\\ \t\r\n") || strings.Contains(prefix, "//") || strings.Contains(prefix, "..") {
		return fmt.Errorf("desktop API prefix must not contain query, fragment, backslash or parent segments")
	}
	if path.Clean(prefix) != prefix || prefix == "/" {
		return fmt.Errorf("desktop API prefix must be a clean path below the root")
	}
	for _, reserved := range reservedTopLevelPaths {
		if pathOverlaps(prefix, reserved) {
			return fmt.Errorf("desktop API prefix conflicts with reserved path %s", reserved)
		}
	}
	return nil
}

// pathOverlaps 报告两个路径层级是否互为祖先关系。
func pathOverlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// apiPrefixFragment 把前缀编码为前端可解析的 fragment 值。
func apiPrefixFragment(prefix string) string {
	return url.Values{"api-prefix": {prefix}}.Encode()
}
