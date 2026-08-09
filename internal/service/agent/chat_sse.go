package agent

import (
	"context"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/global"
)

// CreateAccessLog 创建访问日志
func (s *AgentSvc) ChatSSE(ctx context.Context, dataChan chan *dtochat.ChatSSEResp, req *dtochat.ChatReq) {

	var (
		query = db.EntClient.KaguyaModelsInfo.Query().
			Where(kaguyamodelsinfo.IsDefault(consts.IsTrue)).
			Where(kaguyamodelsinfo.DeletedAtIsNil())
		resp = &dtochat.ChatSSEResp{}
	)

	// 查询数据库的默认模型
	defaultModel, qerr := query.First(ctx)
	if qerr != nil {
		global.Logger.Sugar().Errorf("query default model failed , err is %+v", qerr)
		resp.IsError = true
		dataChan <- resp
	}

	_ = defaultModel

}
