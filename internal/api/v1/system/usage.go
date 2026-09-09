package system

import (
	"errors"
	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// SystemTokenUsage
// @Tags System
// @Summary Token 用量分析
// @Description start_time/end_time 使用秒级 Unix 时间戳，首尾秒包含，精确按秒筛选并按 UTC 自然日聚合；0 或省略使用默认值（结束为今天 UTC 日末，开始为结束日向前一年加一天的日初），最多跨366个 UTC 自然日。不接受日期字符串、负数、毫秒时间戳或反向范围。统计完整聊天轮次（含已删除会话）。会话去重计数；每日活动补零；模型/厂商按用量倒序最多6项。输出已扣除思考，输入含缓存写入。未包含标题任务及失败/取消调用。
// @Param data query dtosystem.TokenUsageReq true "秒级 Unix 时间戳范围（首尾秒包含）"
// @Success 200 {object} dtocode.Response{data=dtosystem.TokenUsageResp}
// @Router /v1/system/usage [get]
func (b *SystemApiV1Group) SystemTokenUsage(c *gin.Context) {
	var req dtosystem.TokenUsageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	// 旧日期参数不能被当作未知参数忽略，否则会静默查询默认范围。
	if c.Request.URL.Query().Has("start") || c.Request.URL.Query().Has("end") {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := systemsvc.TokenUsage(c.Request.Context(), &req)
	if errors.Is(err, servicesystem.ErrInvalidUsageRange) {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err != nil {
		global.Logger.Sugar().Errorf("Token usage query failed: %v", err)
		dtocode.SystemFailure.Failure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}
