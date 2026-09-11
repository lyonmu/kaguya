package project

import (
	"errors"
	"os"

	"github.com/gin-gonic/gin"
	code "github.com/lyonmu/kaguya/internal/dto/code"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
	"github.com/lyonmu/kaguya/internal/global"
	service "github.com/lyonmu/kaguya/internal/service/project"
)

type ProjectApiV1Group struct{}

var svc = &service.ProjectSvc{}

func failure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrPathExists):
		global.Logger.Sugar().Warnf("duplicate project directory: %v", err)
		code.ProjectPathInUse.Failure(c)
	case errors.Is(err, service.ErrNotFound):
		global.Logger.Sugar().Warnf("project not found: %v", err)
		code.ProjectNotFound.Failure(c)
	case errors.Is(err, service.ErrNotGit):
		code.ProjectNotGit.Failure(c)
	case errors.Is(err, service.ErrInvalid), errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission):
		global.Logger.Sugar().Warnf("invalid project request: %v", err)
		code.ProjectParameterError.Failure(c)
	default:
		global.Logger.Sugar().Errorf("project operation failed: %v", err)
		code.ProjectOperationFailure.Failure(c)
	}
}

// ProjectPage
// @Tags Project
// @Summary 项目分页列表
// @Param data query dto.PageReq true "分页/名称前缀"
// @Success 200 {object} code.Response{data=dto.PageResp}
// @Router /v1/project/page [get]
func (*ProjectApiV1Group) ProjectPage(c *gin.Context) {
	var req dto.PageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Page(c.Request.Context(), &req)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectDirectories
// @Tags Project
// @Summary 浏览运行用户主目录内的文件夹
// @Param data query dto.DirectoryReq false "空路径从主目录开始"
// @Success 200 {object} code.Response{data=dto.DirectoryResp}
// @Router /v1/project/directories [get]
func (*ProjectApiV1Group) ProjectDirectories(c *gin.Context) {
	var req dto.DirectoryReq
	if err := c.ShouldBindQuery(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Directories(c.Request.Context(), req.Path)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectDetail
// @Tags Project
// @Summary 项目详情
// @Param id path string true "项目 ID"
// @Success 200 {object} code.Response{data=dto.Resp}
// @Router /v1/project/{id} [get]
func (*ProjectApiV1Group) ProjectDetail(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Detail(c.Request.Context(), req.ID)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectCreate
// @Tags Project
// @Summary 新建主机目录项目（不创建主机文件夹）
// @Description 名称可重复；未删除项目的规范化绝对路径必须唯一，冲突返回业务码 107003
// @Param data body dto.SaveReq true "项目信息"
// @Success 200 {object} code.Response{data=dto.Resp}
// @Router /v1/project [post]
func (*ProjectApiV1Group) ProjectCreate(c *gin.Context) { save(c, "") }

// ProjectUpdate
// @Tags Project
// @Summary 更新项目
// @Description 名称可重复；不能修改为其他未删除项目使用的目录，冲突返回业务码 107003
// @Param id path string true "项目 ID"
// @Param data body dto.SaveReq true "项目信息"
// @Success 200 {object} code.Response{data=dto.Resp}
// @Router /v1/project/{id} [put]
func (*ProjectApiV1Group) ProjectUpdate(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	save(c, req.ID)
}
func save(c *gin.Context, id string) {
	var req dto.SaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Save(c.Request.Context(), id, &req)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectDelete
// @Tags Project
// @Summary 删除项目并解除对话归属，保留历史和主机文件
// @Param id path string true "项目 ID"
// @Success 200 {object} code.Response
// @Router /v1/project/{id} [delete]
func (*ProjectApiV1Group) ProjectDelete(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	if err := svc.Delete(c.Request.Context(), req.ID); err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(nil, c)
}

// ProjectFiles
// @Tags Project
// @Summary 搜索项目中的文件，用于输入框 @ 引用
// @Param id path string true "项目 ID"
// @Param query query string false "文件名或相对路径"
// @Success 200 {object} code.Response{data=service.FileSearchResp}
// @Router /v1/project/{id}/files [get]
func (*ProjectApiV1Group) ProjectFiles(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Files(c.Request.Context(), req.ID, c.Query("query"))
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}
