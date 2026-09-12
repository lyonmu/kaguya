package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemInfo
// @Tags System Info
// @Summary 读取全局系统配置
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemInfoResp}
// @Router /v1/system/info [get]
func (b *SystemApiV1Group) SystemInfo(c *gin.Context) {
	resp, err := systemsvc.Info(c.Request.Context())
	if err != nil {
		global.Logger.Sugar().Errorf("query system info failed: %v", err)
		dtocode.SystemInfoQueryFailure.Failure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemInfoUpdate
// @Tags System Info
// @Summary 保存全局系统配置
// @Description 默认模型及任务模型使用本地记录 ID，空字符串取消选择。User-Agent 必须为非空可打印 ASCII。保存后新发起的聊天和标题请求立即生效，不影响正在执行的请求。
// @Param data body dtosystem.SystemInfoSaveReq true "系统配置"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemInfoResp}
// @Router /v1/system/info [put]
func (b *SystemApiV1Group) SystemInfoUpdate(c *gin.Context) {
	var req dtosystem.SystemInfoSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.InfoUpdate(c.Request.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrInvalidSystemInfo):
			dtocode.RequestParameterError.Failure(c)
		case errors.Is(err, servicesystem.ErrModelNotFound):
			dtocode.ModelNotFound.Failure(c)
		default:
			global.Logger.Sugar().Errorf("save system info failed: %v", err)
			dtocode.SystemInfoUpdateFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}
