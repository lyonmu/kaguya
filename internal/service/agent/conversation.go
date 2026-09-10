package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"entgo.io/ent/dialect/sql"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrConversationBusy     = errors.New("conversation is running or has changed")
	ErrConversationUpdate   = errors.New("invalid conversation update")
)

// 同一进程跨 SSE/WS 连接也不能同时生成同一会话；数据库版本条件再防多实例覆盖。
var activeConversations = struct {
	sync.Mutex
	ids map[string]bool
}{ids: make(map[string]bool)}

func acquireConversation(id string) (func(), error) {
	activeConversations.Lock()
	defer activeConversations.Unlock()
	if activeConversations.ids[id] {
		return nil, ErrConversationBusy
	}
	activeConversations.ids[id] = true
	return func() { activeConversations.Lock(); delete(activeConversations.ids, id); activeConversations.Unlock() }, nil
}

// 未完成的新会话不会有数据库行，可用首次返回的 ID 重试；软删除会话不可恢复。
func loadConversation(ctx context.Context, id string) ([]fantasy.Message, int64, error) {
	row, err := db.EntClient.KaguyaConversation.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if row.DeletedAt != nil {
		return nil, 0, ErrConversationNotFound
	}
	turns, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.TurnIndexLTE(row.TurnCount)).
		Select(kaguyachatturn.FieldMessages, kaguyachatturn.FieldContextMessages, kaguyachatturn.FieldTurnIndex).
		Order(kaguyachatturn.ByTurnIndex()).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	messages := make([]fantasy.Message, 0)
	for _, turn := range turns {
		if turn.ContextMessages != nil {
			messages = append([]fantasy.Message{}, turn.ContextMessages...)
		} else {
			messages = append(messages, turn.Messages...)
		}
	}
	return messages, row.TurnCount, nil
}

type completedTurn struct {
	ContextMessages                                           []fantasy.Message
	CompactionCount                                           int
	ProjectID                                                 string
	ConversationID                                            string
	Version                                                   int64
	UserContent                                               string
	ProviderID, ProviderName, ModelID, ModelName, APIProtocol string
	StartedAt, FinishedAt                                     time.Time
	FinishReason                                              string
	ContextTokens                                             *int64
	ContextWindow                                             int
	Usage                                                     token.NormalizedUsage
	Messages                                                  []fantasy.Message
	Blocks                                                    []dtochat.StoredBlock
}

func saveCompletedTurn(ctx context.Context, turn completedTurn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	client := tx.Client()
	if turn.Version == 0 {
		create := client.KaguyaConversation.Create().SetID(turn.ConversationID).SetTitle(defaultConversationTitle).
			SetModelID(turn.ModelID).SetModelName(turn.ModelName).SetLastMessageAt(turn.FinishedAt)
		if turn.ProjectID != "" {
			if err := projectsvc.Lock(ctx, client, turn.ProjectID); err != nil {
				return err
			}
			create.SetProjectID(turn.ProjectID)
		}
		_, err = create.Save(ctx)
		if err != nil {
			return err
		}
	}
	var toolCalls int64
	for _, b := range turn.Blocks {
		if b.Type == dtochat.BlockTypeToolCall {
			toolCalls++
		}
	}
	duration := max(turn.FinishedAt.Sub(turn.StartedAt).Milliseconds(), 0)
	// 更新父行并锁定写入，确保读取旧上下文的并发生成不能静默追加。
	changed, err := client.KaguyaConversation.Update().
		Where(kaguyaconversation.IDEQ(turn.ConversationID), kaguyaconversation.DeletedAtIsNil(), kaguyaconversation.TurnCountEQ(turn.Version)).
		AddTurnCount(1).SetLastMessageAt(turn.FinishedAt).SetModelID(turn.ModelID).SetModelName(turn.ModelName).
		AddDurationMs(duration).AddToolCalls(toolCalls).AddInputTokens(turn.Usage.InputTokens).AddOutputTokens(turn.Usage.OutputTokens).
		AddTotalTokens(turn.Usage.TotalTokens).AddCachedTokens(turn.Usage.CacheHitTokens).AddReasoningTokens(turn.Usage.ReasoningTokens).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConversationBusy
	}
	row, err := client.KaguyaChatTurn.Create().SetConversationID(turn.ConversationID).SetTurnIndex(turn.Version + 1).
		SetUserContent(turn.UserContent).SetProviderID(turn.ProviderID).SetProviderName(turn.ProviderName).
		SetModelID(turn.ModelID).SetModelName(turn.ModelName).SetAPIProtocol(turn.APIProtocol).
		SetStartedAt(turn.StartedAt.UTC()).SetFinishedAt(turn.FinishedAt.UTC()).SetDurationMs(duration).SetToolCalls(toolCalls).
		SetFinishReason(turn.FinishReason).SetInputTokens(turn.Usage.InputTokens).SetOutputTokens(turn.Usage.OutputTokens).
		SetTotalTokens(turn.Usage.TotalTokens).SetCachedTokens(turn.Usage.CacheHitTokens).SetReasoningTokens(turn.Usage.ReasoningTokens).
		SetNillableContextTokens(turn.ContextTokens).SetContextWindow(turn.ContextWindow).
		SetContextMessages(turn.ContextMessages).SetCompactionCount(turn.CompactionCount).
		SetMessages(turn.Messages).Save(ctx)
	if err != nil {
		return err
	}
	for _, b := range turn.Blocks {
		create := client.KaguyaChatBlock.Create().SetTurnID(row.ID).SetSequence(b.Sequence).SetType(kaguyachatblock.Type(b.Type)).
			SetText(b.Text).SetToolCallID(b.ToolCallID).SetToolName(b.ToolName).SetInput(b.Input).
			SetProviderExecuted(b.ProviderExecuted).SetIsError(b.IsError).SetErrorMessage(b.ErrorMessage).
			SetStartedAt(b.StartedAt).SetFinishedAt(b.FinishedAt).SetStartOrder(b.StartOrder).SetEndOrder(b.EndOrder)
		if b.Output != nil {
			output, err := json.Marshal(b.Output)
			if err != nil {
				return err
			}
			create.SetOutput(output)
		}
		if err := create.Exec(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func conversationResp(row *ent.KaguyaConversation) dtochat.ConversationResp {
	return dtochat.ConversationResp{IsProject: row.ProjectID != nil, ProjectID: row.ProjectID, ID: row.ID, Title: row.Title, Favorite: row.Favorite, TurnCount: row.TurnCount,
		ModelID: row.ModelID, ModelName: row.ModelName, CreatedAt: row.CreatedAt, LastMessageAt: row.LastMessageAt,
		DurationMS: row.DurationMs, ToolCalls: row.ToolCalls,
		Usage: dtochat.Usage{InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens), TotalTokens: int(row.TotalTokens), CachedTokens: int(row.CachedTokens), ReasoningTokens: int(row.ReasoningTokens)}}
}
func (s *AgentSvc) ConversationPage(ctx context.Context, req *dtochat.ConversationPageReq) (*dtochat.ConversationListResp, error) {
	q := db.EntClient.KaguyaConversation.Query().Where(kaguyaconversation.DeletedAtIsNil())
	if req.ProjectID != "" {
		if req.IsProject != nil && !*req.IsProject {
			return nil, ErrConversationUpdate
		}
		if _, err := (&projectsvc.ProjectSvc{}).Detail(ctx, req.ProjectID); err != nil {
			return nil, err
		}
		q.Where(kaguyaconversation.ProjectIDEQ(req.ProjectID))
	} else if req.IsProject != nil && *req.IsProject {
		q.Where(kaguyaconversation.ProjectIDNotNil())
	} else {
		q.Where(kaguyaconversation.ProjectIDIsNil())
	}
	if req.Keyword != "" {
		q.Where(kaguyaconversation.TitleHasPrefix(req.Keyword))
	}
	if req.Favorite != nil {
		q.Where(kaguyaconversation.FavoriteEQ(*req.Favorite))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Order(kaguyaconversation.ByLastMessageAt(sql.OrderDesc()), kaguyaconversation.ByID(sql.OrderDesc())).
		Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtochat.ConversationListResp{Items: make([]dtochat.ConversationResp, 0, len(rows)), Total: total, Page: req.Page, PageSize: req.PageSize}
	for _, row := range rows {
		resp.Items = append(resp.Items, conversationResp(row))
	}
	return resp, nil
}
func (s *AgentSvc) ConversationDetail(ctx context.Context, id string) (*dtochat.ConversationResp, error) {
	row, err := db.EntClient.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(id), kaguyaconversation.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	resp := conversationResp(row)
	return &resp, nil
}
func (s *AgentSvc) ConversationUpdate(ctx context.Context, id string, req *dtochat.ConversationUpdateReq) (*dtochat.ConversationResp, error) {
	if req.Title == nil && req.Favorite == nil {
		return nil, ErrConversationUpdate
	}
	if req.Title != nil && (strings.TrimSpace(*req.Title) == "" || len([]rune(*req.Title)) > 200) {
		return nil, ErrConversationUpdate
	}
	release, err := acquireConversation(id)
	if err != nil {
		return nil, err
	}
	defer release()
	update := db.EntClient.KaguyaConversation.UpdateOneID(id).Where(kaguyaconversation.DeletedAtIsNil())
	if req.Title != nil {
		update.SetTitle(strings.TrimSpace(*req.Title))
	}
	if req.Favorite != nil {
		update.SetFavorite(*req.Favorite)
	}
	row, err := update.Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	resp := conversationResp(row)
	return &resp, nil
}
func (s *AgentSvc) ConversationDelete(ctx context.Context, id string) error {
	release, err := acquireConversation(id)
	if err != nil {
		return err
	}
	defer release()
	err = db.EntClient.KaguyaConversation.UpdateOneID(id).Where(kaguyaconversation.DeletedAtIsNil()).SetDeletedAt(time.Now()).Exec(ctx)
	if ent.IsNotFound(err) {
		return ErrConversationNotFound
	}
	return err
}
func (s *AgentSvc) ConversationTurns(ctx context.Context, id string, req *dtochat.TurnPageReq) (*dtochat.TurnListResp, error) {
	if req.Limit < 1 || req.Limit > 100 || req.Page < 0 || req.Before < 0 || (req.Page > 0 && req.Before > 0) {
		return nil, ErrConversationUpdate
	}
	conv, err := s.ConversationDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	q := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.TurnIndexLTE(conv.TurnCount))
	totalPages := int((conv.TurnCount + int64(req.Limit) - 1) / int64(req.Limit))
	page := req.Page
	if page > 0 {
		page = min(page, max(totalPages, 1))
		q.Where(kaguyachatturn.TurnIndexGT(int64(page-1)*int64(req.Limit)), kaguyachatturn.TurnIndexLTE(int64(page)*int64(req.Limit)))
	} else if req.Before > 0 {
		q.Where(kaguyachatturn.TurnIndexLT(req.Before))
	}
	// 展示查询不读取体积较大且包含 provider 私有元数据的模型上下文。
	fields := make([]string, 0, len(kaguyachatturn.Columns))
	for _, f := range kaguyachatturn.Columns {
		if f != kaguyachatturn.FieldMessages {
			fields = append(fields, f)
		}
	}
	rows, err := q.Select(fields...).Order(kaguyachatturn.ByTurnIndex(sql.OrderDesc())).Limit(req.Limit + 1).
		WithBlocks(func(q *ent.KaguyaChatBlockQuery) { q.Order(kaguyachatblock.BySequence()) }).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtochat.TurnListResp{Items: make([]dtochat.StoredTurn, 0), HasMore: len(rows) > req.Limit,
		Total: conv.TurnCount, Page: page, PageSize: req.Limit, TotalPages: totalPages}
	if resp.HasMore {
		rows = rows[:req.Limit]
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		turn := dtochat.StoredTurn{TurnIndex: row.TurnIndex, UserContent: row.UserContent, ProviderName: row.ProviderName,
			ModelID: row.ModelID, ModelName: row.ModelName, APIProtocol: row.APIProtocol, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
			DurationMS: row.DurationMs, ToolCalls: row.ToolCalls, FinishReason: row.FinishReason,
			Usage:  dtochat.Usage{InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens), TotalTokens: int(row.TotalTokens), CachedTokens: int(row.CachedTokens), ReasoningTokens: int(row.ReasoningTokens)},
			Blocks: make([]dtochat.StoredBlock, 0, len(row.Edges.Blocks))}
		for _, b := range row.Edges.Blocks {
			block := dtochat.StoredBlock{Sequence: b.Sequence, Type: dtochat.BlockType(b.Type), Text: b.Text, ToolCallID: b.ToolCallID,
				ToolName: b.ToolName, Input: b.Input, ProviderExecuted: b.ProviderExecuted, IsError: b.IsError, ErrorMessage: b.ErrorMessage,
				StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, StartOrder: b.StartOrder, EndOrder: b.EndOrder}
			if len(b.Output) > 0 {
				if err := json.Unmarshal(b.Output, &block.Output); err != nil {
					return nil, fmt.Errorf("decode stored tool output: %w", err)
				}
			}
			turn.Blocks = append(turn.Blocks, block)
		}
		resp.Items = append(resp.Items, turn)
	}
	if page > 0 {
		resp.HasMore = page > 1
	}
	if resp.HasMore && len(resp.Items) > 0 {
		resp.NextBefore = resp.Items[0].TurnIndex
	}
	return resp, nil
}
