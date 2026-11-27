package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemWebSearch
// @Tags System Info
// @Summary 读取联网搜索配置
// @Success 200 {object} dtocode.Response{data=dtosystem.WebSearchConfig}
// @Router /v1/system/web-search [get]
func (b *SystemApiV1Group) SystemWebSearch(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resp, err := systemsvc.WebSearchConfig(c.Request.Context())
	if err != nil {
		global.Logger.Error("query web search configuration failed")
		dtocode.SystemInfoQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemWebSearchUpdate
// @Tags System Info
// @Summary 保存联网搜索配置
// @Description 一个开关同时控制网页搜索与抓取，保存后下一轮生效。API Key 留空时保留已有值。
// @Param data body dtosystem.WebSearchSaveReq true "联网搜索配置"
// @Success 200 {object} dtocode.Response{data=dtosystem.WebSearchConfig}
// @Router /v1/system/web-search [put]
func (b *SystemApiV1Group) SystemWebSearchUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dtosystem.WebSearchSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.UpdateWebSearchConfig(c.Request.Context(), &req)
	if errors.Is(err, servicesystem.ErrInvalidSystemInfo) {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err != nil {
		global.Logger.Error("update web search configuration failed")
		dtocode.SystemInfoUpdateFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}
