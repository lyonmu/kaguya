package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemModelPage
// @Tags System Model
// @Summary 获取模型分页列表
// @Param data query dtosystem.SystemModelPageReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemModelListResp}
// @Router /v1/system/model/page [get]
func (b *SystemApiV1Group) SystemModelPage(c *gin.Context) {
	var req dtosystem.SystemModelPageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		global.Logger.Sugar().Warnf("bind model page request failed: %v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ModelPage(c.Request.Context(), &req)
	if err != nil {
		dtocode.ModelQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemModelDetail
// @Tags System Model
// @Summary 获取模型详情
// @Param id path string true "模型 ID"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemModelResp}
// @Router /v1/system/model/{id} [get]
func (b *SystemApiV1Group) SystemModelDetail(c *gin.Context) {
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ModelDetail(c.Request.Context(), req.ID)
	if err != nil {
		if errors.Is(err, servicesystem.ErrModelNotFound) {
			dtocode.ModelNotFound.Failure(c)
		} else {
			dtocode.ModelQueryFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemModelCreate
// @Tags System Model
// @Summary 创建模型
// @Param data body dtosystem.SystemModelSaveReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemModelResp}
// @Router /v1/system/model [post]
func (b *SystemApiV1Group) SystemModelCreate(c *gin.Context) {
	var req dtosystem.SystemModelSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Warnf("bind model create request failed: %v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ModelCreate(c.Request.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrProviderNotFound):
			dtocode.ProviderNotFound.Failure(c)
		case errors.Is(err, servicesystem.ErrModelDuplicate):
			dtocode.ModelIDAlreadyExist.Failure(c)
		default:
			dtocode.ModelCreateFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemModelUpdate
// @Tags System Model
// @Summary 修改模型
// @Param id path string true "模型 ID"
// @Param data body dtosystem.SystemModelSaveReq true "请求参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemModelResp}
// @Router /v1/system/model/{id} [put]
func (b *SystemApiV1Group) SystemModelUpdate(c *gin.Context) {
	var uri dtosystem.SystemIDReq
	var req dtosystem.SystemModelSaveReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Warnf("bind model update request failed: id=%s, err=%v", uri.ID, err)
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ModelUpdate(c.Request.Context(), uri.ID, &req)
	if err != nil {
		switch {
		case errors.Is(err, servicesystem.ErrModelNotFound):
			dtocode.ModelNotFound.Failure(c)
		case errors.Is(err, servicesystem.ErrProviderNotFound):
			dtocode.ProviderNotFound.Failure(c)
		case errors.Is(err, servicesystem.ErrModelDuplicate):
			dtocode.ModelIDAlreadyExist.Failure(c)
		default:
			dtocode.ModelUpdateFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemModelDelete
// @Tags System Model
// @Summary 删除模型
// @Param id path string true "模型 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/system/model/{id} [delete]
func (b *SystemApiV1Group) SystemModelDelete(c *gin.Context) {
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := systemsvc.ModelDelete(c.Request.Context(), req.ID); err != nil {
		if errors.Is(err, servicesystem.ErrModelNotFound) {
			dtocode.ModelNotFound.Failure(c)
		} else {
			dtocode.ModelDeleteFailure.Failure(c)
		}
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// SystemModelLabels
// @Tags System Model
// @Summary 获取模型下拉选项
// @Param data query dtosystem.SystemModelLabelReq false "请求参数"
// @Success 200 {object} dtocode.Response{data=[]dtosystem.SystemModelLabelResp}
// @Router /v1/system/model/label [get]
func (b *SystemApiV1Group) SystemModelLabels(c *gin.Context) {
	var req dtosystem.SystemModelLabelReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.ModelLabels(c.Request.Context(), &req)
	if err != nil {
		dtocode.ModelQueryFailure.Failure(c)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}
