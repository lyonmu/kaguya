package system

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

func mcpFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, servicesystem.ErrMCPNotFound):
		dtocode.MCPNotFound.Failure(c)
	case errors.Is(err, servicesystem.ErrMCPDuplicate):
		dtocode.MCPDuplicate.Failure(c)
	case errors.Is(err, servicesystem.ErrMCPConnect):
		dtocode.Response{Code: dtocode.MCPConnectFailure.Code, Message: err.Error()}.Failure(c)
	case errors.Is(err, servicesystem.ErrMCPInvalid):
		dtocode.Response{Code: 105004, Message: err.Error()}.Failure(c)
	default:
		dtocode.MCPFailure.Failure(c)
	}
}
func mcpID(c *gin.Context) (string, bool) {
	var req dtosystem.SystemIDReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return "", false
	}
	return req.ID, true
}

// SystemMCPPage
// @Tags System MCP
// @Summary 分页查询 MCP 配置与运行状态（不包含环境变量和请求头）
// @Param data query dtosystem.SystemMCPPageReq true "查询参数"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemMCPListResp}
// @Router /v1/system/mcp/page [get]
func (b *SystemApiV1Group) SystemMCPPage(c *gin.Context) {
	var req dtosystem.SystemMCPPageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.MCPPage(c.Request.Context(), &req)
	if err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemMCPDetail
// @Tags System MCP
// @Summary 查询 MCP 配置详情
// @Param id path string true "MCP ID"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemMCPResp}
// @Router /v1/system/mcp/{id} [get]
func (b *SystemApiV1Group) SystemMCPDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := mcpID(c)
	if !ok {
		return
	}
	resp, err := systemsvc.MCPDetail(c.Request.Context(), id)
	if err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemMCPCreate
// @Tags System MCP
// @Summary 创建 MCP 配置（默认停用）
// @Param data body dtosystem.SystemMCPSaveReq true "MCP 配置"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemMCPResp}
// @Router /v1/system/mcp [post]
func (b *SystemApiV1Group) SystemMCPCreate(c *gin.Context) {
	var req dtosystem.SystemMCPSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.MCPCreate(c.Request.Context(), &req)
	if err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemMCPUpdate
// @Tags System MCP
// @Summary 修改 MCP 配置，已启用的服务自动替换连接
// @Param id path string true "MCP ID"
// @Param data body dtosystem.SystemMCPSaveReq true "MCP 配置"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemMCPResp}
// @Router /v1/system/mcp/{id} [put]
func (b *SystemApiV1Group) SystemMCPUpdate(c *gin.Context) {
	id, ok := mcpID(c)
	if !ok {
		return
	}
	var req dtosystem.SystemMCPSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.MCPUpdate(c.Request.Context(), id, &req)
	if err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemMCPSetEnabled
// @Tags System MCP
// @Summary 动态启停 MCP 服务；启用异常服务时重新连接
// @Param id path string true "MCP ID"
// @Param data body dtosystem.SystemMCPStateReq true "启用状态"
// @Success 200 {object} dtocode.Response{data=dtosystem.SystemMCPResp}
// @Router /v1/system/mcp/{id}/state [put]
func (b *SystemApiV1Group) SystemMCPSetEnabled(c *gin.Context) {
	id, ok := mcpID(c)
	if !ok {
		return
	}
	var req dtosystem.SystemMCPStateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.MCPSetEnabled(c.Request.Context(), id, *req.Enabled)
	if err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// SystemMCPDelete
// @Tags System MCP
// @Summary 删除 MCP 配置并关闭连接
// @Param id path string true "MCP ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/system/mcp/{id} [delete]
func (b *SystemApiV1Group) SystemMCPDelete(c *gin.Context) {
	id, ok := mcpID(c)
	if !ok {
		return
	}
	if err := systemsvc.MCPDelete(c.Request.Context(), id); err != nil {
		mcpFailure(c, err)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}
