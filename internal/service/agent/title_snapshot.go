package agent

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
)

// ConversationTitleSnapshots 在同一短只读事务内读取已存在实体，不生成任务或标题。
// 去重前限制请求规模；两次批量查询只投影同步所需字段，不读取正文和工具参数。
func (s *AgentSvc) ConversationTitleSnapshots(ctx context.Context, req *dtochat.TitleSnapshotsReq) (*dtochat.TitleSnapshotsResp, error) {
	if req == nil || len(req.IDs) == 0 || len(req.IDs) > 200 {
		return nil, ErrConversationUpdate
	}
	ids := make([]string, 0, len(req.IDs))
	seen := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 64 || strings.ContainsRune(id, '\x00') {
			return nil, ErrConversationUpdate
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	tx, err := db.EntClient.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	rows, err := client.KaguyaConversation.Query().Where(kaguyaconversation.IDIn(ids...), kaguyaconversation.DeletedAtIsNil()).
		Select(kaguyaconversation.FieldID, kaguyaconversation.FieldTitle, kaguyaconversation.FieldTitleRevision).All(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]dtochat.TitleSnapshot, len(rows))
	visibleIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		visibleIDs = append(visibleIDs, row.ID)
		byID[row.ID] = dtochat.TitleSnapshot{ID: row.ID, Title: row.Title, TitleRevision: row.TitleRevision}
	}
	if len(visibleIDs) > 0 {
		jobs, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDIn(visibleIDs...)).
			Select(kaguyatitlejob.FieldConversationID, kaguyatitlejob.FieldStatus, kaguyatitlejob.FieldRevision).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			item := byID[job.ConversationID]
			status := string(job.Status)
			item.JobStatus, item.JobRevision = &status, job.Revision
			byID[job.ConversationID] = item
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	resp := &dtochat.TitleSnapshotsResp{Items: make([]dtochat.TitleSnapshot, 0, len(rows)), MissingIDs: make([]string, 0)}
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			resp.Items = append(resp.Items, item)
		} else {
			resp.MissingIDs = append(resp.MissingIDs, id)
		}
	}
	return resp, nil
}
