package project

import (
	"github.com/gin-gonic/gin"
	code "github.com/lyonmu/kaguya/internal/dto/code"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
)

// ProjectTree
// @Tags Project
// @Summary 项目文件树，按 .gitignore / .dockerignore 过滤并跳过 .git 与依赖产物目录
// @Description 单次最多返回 5000 个节点，超出时 truncated=true；路径始终相对项目根
// @Param id path string true "项目 ID"
// @Success 200 {object} code.Response{data=dto.TreeResp}
// @Router /v1/project/{id}/tree [get]
func (*ProjectApiV1Group) ProjectTree(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Tree(c.Request.Context(), req.ID)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectContent
// @Tags Project
// @Summary 读取项目内单个文本文件
// @Description 最大读取 512KB，超出截断；二进制返回 binary=true 且不带内容
// @Param id path string true "项目 ID"
// @Param data query dto.ContentReq true "文件相对路径"
// @Success 200 {object} code.Response{data=dto.ContentResp}
// @Router /v1/project/{id}/content [get]
func (*ProjectApiV1Group) ProjectContent(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	var query dto.ContentReq
	if err := c.ShouldBindQuery(&query); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.Content(c.Request.Context(), req.ID, query.Path)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}
