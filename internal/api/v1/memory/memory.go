package memory

import (
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/global"
	svcmemory "github.com/lyonmu/kaguya/internal/service/memory"
)

// memoryIDReq 路径参数。
type memoryIDReq struct {
	ID string `uri:"id" binding:"required,max=64"`
}

// memoryRestoreReq 恢复指定修订的请求体。
type memoryRestoreReq struct {
	Version int64 `json:"version" binding:"required,min=1"`
}

// memoryDeleteQuery 删除语义参数。
type memoryDeleteQuery struct {
	Mode string `form:"mode,default=disable" binding:"omitempty,oneof=disable forget"`
}

func memoryFailure(c *gin.Context, err error, fallback dtocode.Response) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, svcmemory.ErrPageNotFound), errors.Is(err, svcmemory.ErrPageVersionGone):
		dtocode.MemoryPageNotFound.Failure(c)
	case errors.Is(err, svcmemory.ErrPageForbidden):
		dtocode.MemoryPageNotFound.Failure(c)
	case errors.Is(err, svcmemory.ErrPageVersionConflict):
		dtocode.MemoryVersionConflict.Failure(c)
	case errors.Is(err, svcmemory.ErrJobNotFound):
		dtocode.MemoryJobNotFound.Failure(c)
	case errors.Is(err, svcmemory.ErrPlanInvalid), errors.Is(err, svcmemory.ErrStaleLease):
		dtocode.RequestParameterError.Failure(c)
	default:
		global.Logger.Sugar().Errorf("memory API failed: %v", err)
		fallback.Failure(c)
	}
	return false
}

// MemoryPageList
// @Tags Memory
// @Summary 记忆页面列表
// @Description 分页、范围、状态、关键词查询；管理界面可显式查看所有范围（聊天自动召回不能如此）。不传 status 时不含已删除占位。
// @Param data query dtomemory.MemoryPageListReq true "分页/筛选"
// @Success 200 {object} dtocode.Response{data=dtomemory.MemoryPageListResp}
// @Router /v1/memory/pages [get]
func (b *MemoryApiV1Group) MemoryPageList(c *gin.Context) {
	var req dtomemory.MemoryPageListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().ListPages(c.Request.Context(), &req)
	if !memoryFailure(c, err, dtocode.MemoryQueryFailure) {
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryPageDetail
// @Tags Memory
// @Summary 记忆页面详情
// @Description 正文、当前版本、主张证据摘要与关联页面；Cache-Control: no-store。
// @Param id path string true "页面 ID"
// @Param version query integer false "指定历史版本；被遗忘清除的版本不可读取"
// @Success 200 {object} dtocode.Response{data=memory.MemoryPageDetail}
// @Router /v1/memory/pages/{id} [get]
func (b *MemoryApiV1Group) MemoryPageDetail(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	var req struct {
		Version int64 `form:"version" binding:"min=0"`
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().ManageDetail(c.Request.Context(), uri.ID, req.Version)
	if !memoryFailure(c, err, dtocode.MemoryQueryFailure) {
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryPageCreate
// @Tags Memory
// @Summary 人工新建记忆
// @Description 用户手写内容同步保存为人工修订，无需任务模型；可选来源引用定位选中文字（选择助手文字只代表用户认可保存）。作用域只允许 personal/shared/未删除项目。
// @Param data body dtomemory.MemoryPageSaveReq true "记忆页面"
// @Success 200 {object} dtocode.Response{data=memory.MemoryPageDetail}
// @Router /v1/memory/pages [post]
func (b *MemoryApiV1Group) MemoryPageCreate(c *gin.Context) {
	var req dtomemory.MemoryPageSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().CreatePage(c.Request.Context(), &req)
	if !memoryFailure(c, err, dtocode.MemoryFailure) {
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryPageUpdate
// @Tags Memory
// @Summary 编辑/置顶/锁定记忆
// @Description 带 expected_version 的人工编辑，冲突返回明确错误而非最后写入者悄悄覆盖；scope_key 仅支持显式提升到 shared。
// @Param id path string true "页面 ID"
// @Param data body dtomemory.MemoryPageUpdateReq true "编辑内容"
// @Success 200 {object} dtocode.Response{data=memory.MemoryPageDetail}
// @Router /v1/memory/pages/{id} [patch]
func (b *MemoryApiV1Group) MemoryPageUpdate(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	var req dtomemory.MemoryPageUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().UpdatePage(c.Request.Context(), uri.ID, &req)
	if !memoryFailure(c, err, dtocode.MemoryFailure) {
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryPageDelete
// @Tags Memory
// @Summary 停用或忘记记忆
// @Description mode=disable 停用（不再召回、可恢复、内容保留）；mode=forget 删除记忆（立即撤出索引并清除正文/修订/证据摘录，保留最小抑制标记）。删除同步生效，不排队等模型决定。
// @Param id path string true "页面 ID"
// @Param data query memory.memoryDeleteQuery true "删除语义"
// @Success 200 {object} dtocode.Response
// @Router /v1/memory/pages/{id} [delete]
func (b *MemoryApiV1Group) MemoryPageDelete(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	var req memoryDeleteQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	err := memorysvc().DeletePage(c.Request.Context(), uri.ID, req.Mode)
	if !memoryFailure(c, err, dtocode.MemoryFailure) {
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// MemoryPageRevisions
// @Tags Memory
// @Summary 修订历史
// @Description 受删除与范围权限约束；被遗忘清除的修订不再返回。
// @Param id path string true "页面 ID"
// @Success 200 {object} dtocode.Response{data=[]dtomemory.MemoryRevisionResp}
// @Router /v1/memory/pages/{id}/revisions [get]
func (b *MemoryApiV1Group) MemoryPageRevisions(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().ManageRevisions(c.Request.Context(), uri.ID)
	if !memoryFailure(c, err, dtocode.MemoryQueryFailure) {
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryPageRestore
// @Tags Memory
// @Summary 恢复历史修订
// @Description 以旧内容新建恢复修订，不回退版本号；被遗忘且已物理清除的修订不能恢复。
// @Param id path string true "页面 ID"
// @Param data body memory.memoryRestoreReq true "目标版本"
// @Success 200 {object} dtocode.Response{data=memory.MemoryPageDetail}
// @Router /v1/memory/pages/{id}/restore [post]
func (b *MemoryApiV1Group) MemoryPageRestore(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	var req memoryRestoreReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().ManageRestore(c.Request.Context(), uri.ID, req.Version)
	if !memoryFailure(c, err, dtocode.MemoryFailure) {
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryJobList
// @Tags Memory
// @Summary 记忆任务列表
// @Description 状态、错误码与独立用量成本；不返回原始 Prompt，待审任务附带有界提案。
// @Param data query dtomemory.MemoryJobListReq true "分页/筛选"
// @Success 200 {object} dtocode.Response{data=dtomemory.MemoryJobListResp}
// @Router /v1/memory/jobs [get]
func (b *MemoryApiV1Group) MemoryJobList(c *gin.Context) {
	var req dtomemory.MemoryJobListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().ListJobs(c.Request.Context(), &req)
	if !memoryFailure(c, err, dtocode.MemoryQueryFailure) {
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryCompile
// @Tags Memory
// @Summary 立即整理已选择范围
// @Description 登记后由后台 Worker 跳过防抖立即处理该范围的待处理来源；响应不是编译结果。
// @Param data body dtomemory.MemoryCompileReq true "范围"
// @Success 200 {object} dtocode.Response
// @Router /v1/memory/compile [post]
func (b *MemoryApiV1Group) MemoryCompile(c *gin.Context) {
	var req dtomemory.MemoryCompileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := svcmemory.ValidateScope(c.Request.Context(), memorysvc().Client(), req.ScopeKey); err != nil {
		memoryFailure(c, err, dtocode.RequestParameterError)
		return
	}
	svcmemory.RequestCompile(req.ScopeKey)
	dtocode.SystemSuccess.Success(gin.H{"queued": true}, c)
}

// MemoryJobRetry
// @Tags Memory
// @Summary 重试失败/阻塞任务
// @Param id path string true "任务 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/memory/jobs/{id}/retry [post]
func (b *MemoryApiV1Group) MemoryJobRetry(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := memorysvc().RetryJob(c.Request.Context(), uri.ID); err != nil {
		memoryFailure(c, err, dtocode.MemoryJobNotFound)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// MemoryJobApprove
// @Tags Memory
// @Summary 批准待审提案
// @Description 以当前页面版本发布新修订，不回退版本号。
// @Param id path string true "任务 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/memory/jobs/{id}/approve [post]
func (b *MemoryApiV1Group) MemoryJobApprove(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if _, err := memorysvc().ApproveJob(c.Request.Context(), uri.ID); err != nil {
		if errors.Is(err, svcmemory.ErrStaleLease) {
			dtocode.MemoryJobNotReview.Failure(c)
			return
		}
		memoryFailure(c, err, dtocode.MemoryFailure)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// MemoryJobReject
// @Tags Memory
// @Summary 拒绝待审提案
// @Description 记录处理结果，不产生新修订。
// @Param id path string true "任务 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/memory/jobs/{id}/reject [post]
func (b *MemoryApiV1Group) MemoryJobReject(c *gin.Context) {
	var uri memoryIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := memorysvc().RejectJob(c.Request.Context(), uri.ID); err != nil {
		memoryFailure(c, err, dtocode.MemoryFailure)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}

// MemoryStatus
// @Tags Memory
// @Summary 记忆状态与后台任务用量
// @Description pending/blocked/failed 任务、索引版本与独立的后台任务用量标签；编译消费不计入聊天口径。
// @Success 200 {object} dtocode.Response{data=dtomemory.MemoryStatusResp}
// @Router /v1/memory/status [get]
func (b *MemoryApiV1Group) MemoryStatus(c *gin.Context) {
	resp, err := memorysvc().Status(c.Request.Context())
	if !memoryFailure(c, err, dtocode.MemoryQueryFailure) {
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// MemoryExport
// @Tags Memory
// @Summary Markdown 导出
// @Description 用户确认的导出范围；正文可携带，但导出内容不写入临时目录明文副本。
// @Param data body dtomemory.MemoryExportReq true "导出范围"
// @Success 200 {object} dtocode.Response{data=dtomemory.MemoryExportResp}
// @Router /v1/memory/export [post]
func (b *MemoryApiV1Group) MemoryExport(c *gin.Context) {
	var req dtomemory.MemoryExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := memorysvc().Export(c.Request.Context(), &req)
	if !memoryFailure(c, err, dtocode.MemoryFailure) {
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}
