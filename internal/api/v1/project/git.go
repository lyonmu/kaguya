package project

import (
	"github.com/gin-gonic/gin"
	code "github.com/lyonmu/kaguya/internal/dto/code"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
)

// ProjectGitStatus
// @Tags Project
// @Summary 项目未提交变更清单
// @Description 非 Git 目录或缺少 git 命令时返回 is_git=false 并携带 message，不视为错误
// @Param id path string true "项目 ID"
// @Success 200 {object} code.Response{data=dto.GitStatusResp}
// @Router /v1/project/{id}/git/status [get]
func (*ProjectApiV1Group) ProjectGitStatus(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.GitStatus(c.Request.Context(), req.ID)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}

// ProjectGitDiff
// @Tags Project
// @Summary 单个变更文件的统一 diff
// @Description 输出相对 HEAD 的 diff；未跟踪文件按新增内容输出，最大 1MB，超出截断。非 Git 项目返回业务码 103004
// @Param id path string true "项目 ID"
// @Param data query dto.GitDiffReq true "文件相对路径"
// @Success 200 {object} code.Response{data=dto.GitDiffResp}
// @Router /v1/project/{id}/git/diff [get]
func (*ProjectApiV1Group) ProjectGitDiff(c *gin.Context) {
	var req dto.IDReq
	if err := c.ShouldBindUri(&req); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	var query dto.GitDiffReq
	if err := c.ShouldBindQuery(&query); err != nil {
		code.RequestParameterError.Failure(c)
		return
	}
	resp, err := svc.GitDiff(c.Request.Context(), req.ID, query.Path)
	if err != nil {
		failure(c, err)
		return
	}
	code.SystemSuccess.Success(resp, c)
}
