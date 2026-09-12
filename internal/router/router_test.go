package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

// 已删除的接口必须返回 404，不能经 SPA fallback 返回 200 HTML。
func TestRemovedRoutesReturnNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalPrefix, originalLogger := global.Cfg.RouterPrefix, global.Logger
	global.Cfg.RouterPrefix = "/kaguya/api"
	global.Logger = zap.NewNop()
	t.Cleanup(func() { global.Cfg.RouterPrefix, global.Logger = originalPrefix, originalLogger })

	engine := gin.New()
	InitRouter(engine)

	for _, target := range []string{"/kaguya/api/v1/chat/ws", "/kaguya/api/v1/system/info/tls"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body)
		}
	}
}
