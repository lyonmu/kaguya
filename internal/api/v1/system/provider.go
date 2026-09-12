package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemProviderPage
// @Tags System Provider
// @Summary 获取提供商分页列表
// @Param data query dtosystem.SystemProviderPageReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemProviderListResp}
// @Router /v1/system/provider/page [get]
func (b *SystemApiV1Group) SystemProviderPage(c *gin.Context) {
	// 列表携带掩码化的 API Key，响应不得被浏览器或代理缓存。
	c.Header("Cache-Control", "no-store")
	var req dtosystem.SystemProviderPageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		global.Logger.Sugar().Warnf("bind provider page request failed: %v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderPage(c.Request.Context(), &req)
	if err != nil {
		dtocode.ProviderQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemProviderDetail
// @Tags System Provider
// @Summary 获取提供商详情
// @Param id path string true "提供商 ID"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemProviderResp}
// @Router /v1/system/provider/{id} [get]
func (b *SystemApiV1Group) SystemProviderDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderDetail(c.Request.Context(), req.ID)
	if err != nil {
		if errors.Is(err, servicesystem.ErrProviderNotFound) {
			dtocode.ProviderNotFound.Failure(c)
		} else {
			dtocode.ProviderQueryFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemProviderAPIKey
// @Tags System Provider
// @Summary 查看提供商 API Key 明文（前端显式触发，不进入列表响应）
// @Param id path string true "提供商 ID"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemProviderAPIKeyResp}
// @Router /v1/system/provider/{id}/api-key [get]
func (b *SystemApiV1Group) SystemProviderAPIKey(c *gin.Context) {
	// 明文密钥响应不得被任何层缓存。
	c.Header("Cache-Control", "no-store")
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderAPIKey(c.Request.Context(), req.ID)
	if err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrProviderNotFound):
			dtocode.ProviderNotFound.Failure(c)
		case errors.Is(err, servicesystem.ErrProviderSecret):
			dtocode.ProviderSecretInvalid.Failure(c)
		default:
			dtocode.ProviderQueryFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemProviderCreate
// @Tags System Provider
// @Summary 创建提供商
// @Param data body dtosystem.SystemProviderSaveReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemProviderResp}
// @Router /v1/system/provider [post]
func (b *SystemApiV1Group) SystemProviderCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dtosystem.SystemProviderSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Warnf("bind provider create request failed: %v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderCreate(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, servicesystem.ErrProviderDuplicate) {
			dtocode.ProviderNameAlreadyExist.Failure(c)
		} else {
			dtocode.ProviderCreateFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemProviderUpdate
// @Tags System Provider
// @Summary 修改提供商
// @Param id path string true "提供商 ID"
// @Param data body dtosystem.SystemProviderSaveReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemProviderResp}
// @Router /v1/system/provider/{id} [put]
func (b *SystemApiV1Group) SystemProviderUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var uri dtosystem.SystemIDReq
	var req dtosystem.SystemProviderSaveReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Warnf("bind provider update request failed: id=%s, err=%v", uri.ID, err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderUpdate(c.Request.Context(), uri.ID, &req)
	if err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrProviderNotFound):
			dtocode.ProviderNotFound.Failure(c)
		case errors.Is(err, servicesystem.ErrProviderDuplicate):
			dtocode.ProviderNameAlreadyExist.Failure(c)
		default:
			dtocode.ProviderUpdateFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemProviderDelete
// @Tags System Provider
// @Summary 删除提供商及其模型
// @Param id path string true "提供商 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/system/provider/{id} [delete]
func (b *SystemApiV1Group) SystemProviderDelete(c *gin.Context) {
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := systemsvc.ProviderDelete(c.Request.Context(), req.ID); err != nil {
		if errors.Is(err, servicesystem.ErrProviderNotFound) {
			dtocode.ProviderNotFound.Failure(c)
		} else {
			dtocode.ProviderDeleteFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// SystemProviderLabels
// @Tags System Provider
// @Summary 获取提供商下拉选项
// @Param data query dtosystem.SystemProviderLabelReq false "请求参数"
// @Success 200 {object} dtocode.Response{data=[]dtosystem.SystemProviderLabelResp}
// @Router /v1/system/provider/label [get]
func (b *SystemApiV1Group) SystemProviderLabels(c *gin.Context) {
	var req dtosystem.SystemProviderLabelReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ProviderLabels(c.Request.Context(), &req)
	if err != nil {
		dtocode.ProviderQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}
