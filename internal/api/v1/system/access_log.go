package system

import (
	"errors"
	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
)

// SystemAccessLogPage
// @Tags      System
// @Summary   SystemAccessLogPage
// @Description 获取访问日志分页列表；start_time/end_time 为秒级 Unix 时间戳，首尾秒包含，0 或省略表示不限。不接受日期字符串、负数、毫秒时间戳或反向范围。
// @Param     data  query      dtosystem.SystemAccessLogPageReq  true  "访问日志请求参数"
// @Produce   json
// @Success   200  {object}  dtocode.Response{code=number,data=dtosystem.SystemAccessLogListResp,message=string}  "100000,success"
// @Router    /v1/system/accesslog/page [get]
func (b *SystemApiV1Group) SystemAccessLogPage(c *gin.Context) {
	var req dtosystem.SystemAccessLogPageReq
	var _ dtosystem.SystemAccessLogListResp
	if err := c.ShouldBindQuery(&req); err != nil {
		global.Logger.Sugar().Errorf("Request parameter error : %+v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}

	resp, err := systemsvc.AccessLogPage(c.Request.Context(), &req)
	if errors.Is(err, dtosystem.ErrInvalidTimeRange) {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err != nil {
		global.Logger.Sugar().Errorf("AccessLog query failure : %+v", err)
		dtocode.AccessLogQueryFailure.Failure(c)
		return
	}

	dtocode.SystemSuccess.Success(resp, c)
}
