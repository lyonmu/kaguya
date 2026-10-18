package chat

import (
	"strconv"

	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ConversationPage
// @Tags Chat History
// @Summary 会话列表（含进行中与空会话）
// @Description 按最近完整轮次时间倒序；支持标题前缀搜索、收藏筛选。默认仅普通对话；is_project=true 仅项目对话，project_id 可指定项目，不能与 is_project=false 同时使用。响应 is_project 根据当前项目归属生成。新建会话在首轮正文开始生成前即创建并出现在列表中，失败或取消的轮次保留空会话，可手动删除。
// @Param data query dtochat.ConversationPageReq true "分页/筛选"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationListResp}
// @Router /v1/chat/conversation/page [get]
func (b *ChatApiV1Group) ConversationPage(c *gin.Context) {
	var req dtochat.ConversationPageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationPage(c.Request.Context(), &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationDetail
// @Tags Chat History
// @Summary 会话摘要与累计运行信息（Overview）
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationResp}
// @Router /v1/chat/conversation/{id} [get]
func (b *ChatApiV1Group) ConversationDetail(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationDetail(c.Request.Context(), uri.ID)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationInstructionsUpgrade
// @Tags Chat History
// @Summary 将旧合并指令快照升级为 split（归档旧文本并冻结当前项目指令）
// @Description 仅旧项目会话需要；运行中的会话返回 102002，下一轮生效。旧 agent_instructions 保留为归档不再参与上下文。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationResp}
// @Router /v1/chat/conversation/{id}/instructions/upgrade [post]
func (b *ChatApiV1Group) ConversationInstructionsUpgrade(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationInstructionsUpgrade(c.Request.Context(), uri.ID)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationUpdateFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTitleWait
// @Tags Chat History
// @Summary 等待已有标题任务并返回已保存标题
// @Description 兼容等待接口，不启动生成任务；无任务时直接返回当前标题。仅等待当前进程的任务，最多30秒。新客户端应使用 POST 接口生成标题。不存在或已删除会话返回会话不存在。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationTitleResp}
// @Router /v1/chat/conversation/{id}/title/wait [get]
func (b *ChatApiV1Group) ConversationTitleWait(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTitleWait(c.Request.Context(), uri.ID)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTitleSnapshots
// @Tags Chat History
// @Summary 批量读取标题与任务版本快照
// @Description 只读同步，1–200个ID，去重前限制批次；按请求顺序返回未删除会话，缺失及已删除ID进入missing_ids。无任务时job_status=null、job_revision=0；不创建或生成标题。
// @Param data body dtochat.TitleSnapshotsReq true "已加载会话ID"
// @Success 200 {object} dtocode.Response{data=dtochat.TitleSnapshotsResp}
// @Router /v1/chat/conversation/title-snapshots [post]
func (b *ChatApiV1Group) ConversationTitleSnapshots(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dtochat.TitleSnapshotsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTitleSnapshots(c.Request.Context(), &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTitleGenerate
// @Tags Chat History
// @Summary 默认标题生成或重试，并等待已保存标题
// @Description 每轮成功结束且标题仍为“新对话”时调用一次。新会话创建后已立即并行启动同一任务（只用用户提问，不等回答），模型返回后直接条件写入标题；此处按需补生成或重试，使用全局任务模型根据已保存的最早成功轮次问答生成标题（首轮失败时不会跳过后续成功轮次）。未配置任务模型返回 102007，提供商的 API Key 无法解密返回 102010。已有任务（含提前启动的）则等待，不覆盖非默认标题。最多等待30秒，生成失败或等待超时返回当前标题，不自动重试。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationTitleResp}
// @Router /v1/chat/conversation/{id}/title/wait [post]
func (b *ChatApiV1Group) ConversationTitleGenerate(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTitleGenerate(c.Request.Context(), uri.ID)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTurns
// @Tags Chat History
// @Summary 按原始执行顺序读取完整轮次（聊天/Trace/工具详情）
// @Description 默认每页5轮，上限100轮。page>0 时按时间正序分页（越界页定位末页），与 before 互斥；返回 total/page/page_size/total_pages。page=0 使用游标：首屏最新 limit 轮，用 next_before 向前加载。compact=true 时仅正文完整返回，工具及思考块只返回元信息，details_deferred=true 的块通过单块详情接口加载。每轮 approvals 批量返回只读审计元信息，不附加参数正文；consumed 仅表示授权消费，execution_state 独立表示执行结果，旧 consumed 无结果时间按 unknown 返回。所有结果与 blocks 均正序，不返回模型私有 metadata。
// @Param id path string true "会话雪花 ID"
// @Param data query dtochat.TurnPageReq true "历史游标"
// @Success 200 {object} dtocode.Response{data=dtochat.TurnListResp}
// @Router /v1/chat/conversation/{id}/turns [get]
func (b *ChatApiV1Group) ConversationTurns(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	var req dtochat.TurnPageReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTurns(c.Request.Context(), uri.ID, &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationBlock
// @Tags Chat History
// @Summary 按需读取单个工具或思考内容块
// @Param id path string true "会话雪花 ID"
// @Param turn path int true "轮次索引"
// @Param sequence path int true "轮内内容块序号"
// @Success 200 {object} dtocode.Response{data=dtochat.StoredBlock}
// @Router /v1/chat/conversation/{id}/turns/{turn}/blocks/{sequence} [get]
func (b *ChatApiV1Group) ConversationBlock(c *gin.Context) {
	var req dtochat.BlockDetailReq
	if err := c.ShouldBindUri(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationBlock(c.Request.Context(), &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationUpdate
// @Tags Chat History
// @Summary 修改会话标题或收藏状态
// @Param id path string true "会话雪花 ID"
// @Param data body dtochat.ConversationUpdateReq true "至少提供 title 或 favorite"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationResp}
// @Router /v1/chat/conversation/{id} [put]
func (b *ChatApiV1Group) ConversationUpdate(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	var req dtochat.ConversationUpdateReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationUpdate(c.Request.Context(), uri.ID, &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationUpdateFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationStop
// @Tags Chat
// @Summary 标记用户主动停止
// @Description 记录指定会话当前轮次是用户主动停止，使落库状态区分为 canceled 而不是断联的 interrupted。没有运行轮次时为幂等空操作；取消本身仍由客户端断开流式连接完成。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/chat/conversation/{id}/stop [post]
func (b *ChatApiV1Group) ConversationStop(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	agentvc.StopConversation(uri.ID)
	dtocode.SystemSuccess.Success(nil, c)
}

// ConversationDelete
// @Tags Chat History
// @Summary 软删除会话（删除后不可查询或续聊）
// @Description 删除会话同时撤销该会话记忆来源并取消待处理编译；purge_memory=true 时进一步删除来源及其派生记忆内容（需用户明确确认）。删除记忆不会改写聊天历史。
// @Param id path string true "会话雪花 ID"
// @Param purge_memory query bool false "一并删除该会话的来源及其派生记忆内容"
// @Success 200 {object} dtocode.Response
// @Router /v1/chat/conversation/{id} [delete]
func (b *ChatApiV1Group) ConversationDelete(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	purge := false
	if raw := c.Query("purge_memory"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			dtocode.RequestParameterError.Failure(c)
			return
		}
		purge = parsed
	}
	if err := agentvc.ConversationDelete(c.Request.Context(), uri.ID, purge); err != nil {
		conversationFailure(c, err, dtocode.ConversationDeleteFailure)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}
