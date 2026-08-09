package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/service/system"
	"github.com/lyonmu/quebec/pkg/tools"
)

type AccessLogMiddleware struct {
	systemSvc *system.SystemSvc
}

func NewAccessLogMiddleware() *AccessLogMiddleware {
	return &AccessLogMiddleware{
		systemSvc: &system.SystemSvc{},
	}
}

func (m *AccessLogMiddleware) AccessLog() gin.HandlerFunc {

	return func(c *gin.Context) {
		uaStr := c.Request.UserAgent()
		ua := tools.ParseUserAgent(uaStr)
		access_ip := c.ClientIP()
		browserName, browserVersion := ua.Browser()
		browserEngineName, browserEngineVersion := ua.Engine()

		req := &dtosystem.SystemAccessLogReq{
			AccessIP:             access_ip,
			AccessTime:           time.Now().Unix(),
			Os:                   ua.OS(),
			Platform:             ua.Platform(),
			BrowserName:          browserName,
			BrowserVersion:       browserVersion,
			BrowserEngineName:    browserEngineName,
			BrowserEngineVersion: browserEngineVersion,
		}

		go func() {
			defer func() {
				if r := recover(); r != nil {
					global.Logger.Sugar().Errorf("Access log goroutine panicked: %v", r)
				}
			}()

			// 创建独立的 Context，避免主请求 Context 取消的影响
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := m.systemSvc.CreateAccessLog(ctx, req); err != nil {
				global.Logger.Sugar().Errorf("Failed to create access log : %v", err)
			}
		}()
	}

}
