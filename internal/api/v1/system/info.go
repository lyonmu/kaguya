package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	memorysvc "github.com/lyonmu/kaguya/internal/service/memory"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemInfo
// @Tags System Info
// @Summary 读取全局系统配置
// @Description instruction_status 的 generation 表示已接收配置，loaded_generation 表示已发布配置；last_error 为脱敏失败原因，不返回指令正文。
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

// SystemInfoAgentsPaths
// @Tags System Info
// @Summary 保存全局 AGENTS.md 路径列表（不受通用请求体大小限制）
// @Description 路径顺序即读取顺序；空数组表示不读取任何全局指令。保存成功表示配置已提交；单个后台读取线程验证稳定内容后重建监听并原子发布。通过 instruction_status 区分加载中、已发布和失败保留旧版本。
// @Param data body dtosystem.AgentsPathsReq true "路径列表"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemInfoResp}
// @Router /v1/system/info/agents-paths [put]
func (b *SystemApiV1Group) SystemInfoAgentsPaths(c *gin.Context) {
	var req dtosystem.AgentsPathsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := systemsvc.UpdateAgentsPaths(c.Request.Context(), req.Paths); err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrInvalidSystemInfo):
			dtocode.RequestParameterError.Failure(c)
		default:
			global.Logger.Sugar().Errorf("update agents paths failed: %v", err)
			dtocode.SystemInfoUpdateFailure.Failure(c)
		}
		return
	}
	resp, err := systemsvc.Info(c.Request.Context())
	if err != nil {
		global.Logger.Sugar().Errorf("query system info after paths update failed: %v", err)
		dtocode.SystemInfoQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemInfoUpdate
// @Tags System Info
// @Summary 保存全局系统配置
// @Description 默认模型及任务模型使用本地记录 ID，空字符串取消选择。基础提示词和同步计划保存后对新请求或调度立即生效，不影响正在执行的请求。全局指令路径在后台加载，保存成功不等于新正文已发布；以 instruction_status 为准。
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
	// 任务模型/记忆配置修复后唤醒记忆 Worker：blocked 作业等待明确变化。
	memorysvc.Notify()
	dtocode.SystemSuccess.Success(resp, c)
}
