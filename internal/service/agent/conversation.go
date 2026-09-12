package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// loadConversation 读取可续聊的模型上下文与已完成轮数；会话不存在时返回空历史与 0。
func loadConversation(ctx context.Context, id string) ([]fantasy.Message, int64, error) {
	row, messages, err := loadConversationRow(ctx, id)
	if err != nil || row == nil {
		return messages, 0, err
	}
	return messages, row.TurnCount, nil
}

// loadConversationRow 读取会话行及其可续聊的模型上下文。
// 会话行不存在时返回 nil，调用方据此开启新会话；软删除会话不可恢复。
func loadConversationRow(ctx context.Context, id string) (*ent.KaguyaConversation, []fantasy.Message, error) {
	row, err := db.EntClient.KaguyaConversation.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if row.DeletedAt != nil {
		return nil, nil, ErrConversationNotFound
	}
	// 压缩快照就是完整的续聊上下文，最后一次快照之前的原始消息不再参与拼接；
	// compaction_count>0 与快照由同一 compactor 产生（见 compaction.snapshot），
	// 用它定位最新快照，避免读取快照前的轮次和大字段 messages。
	anchor := int64(0)
	latest, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted), kaguyachatturn.CompactionCountGT(0)).
		Order(ent.Desc(kaguyachatturn.FieldTurnIndex)).Select(kaguyachatturn.FieldTurnIndex).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, nil, err
	}
	if err == nil {
		anchor = latest.TurnIndex
	}
	turns, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.TurnIndexGTE(anchor)).
		Select(kaguyachatturn.FieldTurnIndex, kaguyachatturn.FieldStatus, kaguyachatturn.FieldUserContent,
			kaguyachatturn.FieldMessages, kaguyachatturn.FieldContextMessages).
		Order(kaguyachatturn.ByTurnIndex()).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]fantasy.Message, 0)
	for _, turn := range turns {
		if turn.Status == kaguyachatturn.StatusCompleted {
			if turn.ContextMessages != nil {
				// 完整快照整体替换之前的历史。
				messages = append([]fantasy.Message{}, turn.ContextMessages...)
			} else {
				messages = append(messages, turn.Messages...)
			}
			continue
		}
		// 未完成轮次不进入完整历史，但用户提问一律保留（对齐 pi：user 消息始终在上下文中，
		// 只有 stopReason 为 error/aborted 的不完整助手消息会在发送前被过滤）：
		// 下一轮能看到上一轮的要求，避免“继续”失去指代；
		// 半截助手内容与未闭合的工具调用绝不拼接。
		// 若要改成只恢复异常中断的提问，在此处排除 canceled 状态即可。
		if turn.Status != kaguyachatturn.StatusRunning {
			messages = append(messages, fantasy.NewUserMessage(turn.UserContent))
		}
	}
	return row, messages, nil
}

// createConversation 在首轮正文开始生成前写入会话行，让新会话无需等待轮次完成
// 就出现在列表中；失败或取消的轮次会留下空会话，由用户自行删除，重试沿用同一 ID。
func createConversation(ctx context.Context, target *chatTarget, id, projectID string, startedAt time.Time) error {
	create := db.EntClient.KaguyaConversation.Create().SetID(id).SetTitle(defaultConversationTitle).
		SetModelID(target.model.ModelID).SetModelName(target.model.ModelName).SetLastMessageAt(startedAt)
	if projectID != "" {
		if err := projectsvc.Lock(ctx, db.EntClient, projectID); err != nil {
			return err
		}
		create.SetProjectID(projectID)
	}
	_, err := create.Save(ctx)
	return err
}

// turnStart 是创建进行中占位行的输入。
type turnStart struct {
	ConversationID string
	UserContent    string
	ProviderID, ProviderName, ModelID, ModelName, APIProtocol string
	StartedAt      time.Time
}

// beginTurn 在正文开始生成前写入 running 占位行；中断的轮次同样占用 turn_index，
// 因此续聊历史只读取 completed 状态。
func beginTurn(ctx context.Context, start turnStart) (*ent.KaguyaChatTurn, error) {
	next := int64(1)
	last, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(start.ConversationID)).
		Order(ent.Desc(kaguyachatturn.FieldTurnIndex)).Select(kaguyachatturn.FieldTurnIndex).First(ctx)
	if err == nil {
		next = last.TurnIndex + 1
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	return db.EntClient.KaguyaChatTurn.Create().
		SetConversationID(start.ConversationID).SetTurnIndex(next).SetStatus(kaguyachatturn.StatusRunning).
		SetUserContent(start.UserContent).
		SetProviderID(start.ProviderID).SetProviderName(start.ProviderName).
		SetModelID(start.ModelID).SetModelName(start.ModelName).SetAPIProtocol(start.APIProtocol).
		SetStartedAt(start.StartedAt).SetFinishedAt(start.StartedAt).
		SetDurationMs(0).SetToolCalls(0).SetFinishReason("").
		SetInputTokens(0).SetOutputTokens(0).SetTotalTokens(0).SetCachedTokens(0).SetReasoningTokens(0).
		SetMessages([]fantasy.Message{}).Save(ctx)
}

// markTurnEnd 在生成失败或取消后把占位行标为终态并保留已产生的内容。
// 生成 context 此时可能已被取消，因此使用独立的后台 context。
func markTurnEnd(turnID string, status kaguyachatturn.Status, finishedAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := db.EntClient.KaguyaChatTurn.Get(ctx, turnID)
	if err != nil {
		return err
	}
	duration := max(finishedAt.Sub(row.StartedAt).Milliseconds(), 0)
	_, err = db.EntClient.KaguyaChatTurn.UpdateOneID(turnID).
		SetStatus(status).SetFinishedAt(finishedAt.UTC()).SetDurationMs(duration).Save(ctx)
	return err
}

// ReconcileRunningTurns 在进程启动时把遗留的 running 轮次标记为 interrupted。
// 单实例部署下没有其它进程能继续它们，不清理会让历史永久停留在“正在生成”。
func ReconcileRunningTurns(ctx context.Context) error {
	rows, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusRunning)).
		Select(kaguyachatturn.FieldID).All(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, row := range rows {
		if err := markTurnEnd(row.ID, kaguyachatturn.StatusInterrupted, now); err != nil {
			return err
		}
	}
	return nil
}

type completedTurn struct {
	AgentInstructions                                         *string
	ContextMessages                                           []fantasy.Message
	CompactionCount                                           int
	ProjectID                                                 string
	ConversationID                                            string
	TurnID                                                    string // 进行中占位行的 ID；为空时按旧路径创建新行
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
		// 首轮开始时会话行已由 createConversation 写入；这里只保留兜底创建，
		// 兼容直接写入轮次的路径（如测试），已有行时跳过。
		exists, err := client.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(turn.ConversationID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			create := client.KaguyaConversation.Create().SetID(turn.ConversationID).SetTitle(defaultConversationTitle).
				SetModelID(turn.ModelID).SetModelName(turn.ModelName).SetLastMessageAt(turn.FinishedAt)
			if turn.ProjectID != "" {
				if err := projectsvc.Lock(ctx, client, turn.ProjectID); err != nil {
					return err
				}
				create.SetProjectID(turn.ProjectID)
			}
			if _, err := create.Save(ctx); err != nil {
				// 多实例并发创建同一会话时，后到者按会话繁忙处理。
				if ent.IsConstraintError(err) {
					return ErrConversationBusy
				}
				return err
			}
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
	if turn.AgentInstructions != nil {
		// Preserve even an empty snapshot. Legacy conversations initialize once on
		// their next successful turn, within the same history transaction.
		if _, err := client.KaguyaConversation.Update().Where(kaguyaconversation.IDEQ(turn.ConversationID), kaguyaconversation.AgentInstructionsIsNil()).SetAgentInstructions(*turn.AgentInstructions).Save(ctx); err != nil {
			return err
		}
	}
	createTurn := client.KaguyaChatTurn.Create().SetConversationID(turn.ConversationID).SetTurnIndex(turn.Version + 1).
		SetUserContent(turn.UserContent).SetProviderID(turn.ProviderID).SetProviderName(turn.ProviderName).
		SetModelID(turn.ModelID).SetModelName(turn.ModelName).SetAPIProtocol(turn.APIProtocol).
		SetStatus(kaguyachatturn.StatusCompleted).
		SetStartedAt(turn.StartedAt.UTC()).SetFinishedAt(turn.FinishedAt.UTC()).SetDurationMs(duration).SetToolCalls(toolCalls).
		SetFinishReason(turn.FinishReason).SetInputTokens(turn.Usage.InputTokens).SetOutputTokens(turn.Usage.OutputTokens).
		SetTotalTokens(turn.Usage.TotalTokens).SetCachedTokens(turn.Usage.CacheHitTokens).SetReasoningTokens(turn.Usage.ReasoningTokens).
		SetNillableContextTokens(turn.ContextTokens).SetContextWindow(turn.ContextWindow).
		SetCompactionCount(turn.CompactionCount)
	// 未压缩的轮次不写 context_messages，列保持 SQL NULL，快照存在时才有 JSON 数组。
	if turn.ContextMessages != nil {
		createTurn.SetContextMessages(turn.ContextMessages)
	}
	turnID := turn.TurnID
	if turnID == "" {
		row, err := createTurn.SetMessages(turn.Messages).Save(ctx)
		if err != nil {
			return err
		}
		turnID = row.ID
	} else {
		// 进行中的占位行：补全结束信息并整体替换中间刷入的块。
		update := client.KaguyaChatTurn.UpdateOneID(turnID).Where(kaguyachatturn.ConversationIDEQ(turn.ConversationID)).
			SetUserContent(turn.UserContent).SetProviderID(turn.ProviderID).SetProviderName(turn.ProviderName).
			SetModelID(turn.ModelID).SetModelName(turn.ModelName).SetAPIProtocol(turn.APIProtocol).
			SetStatus(kaguyachatturn.StatusCompleted).
			SetStartedAt(turn.StartedAt.UTC()).SetFinishedAt(turn.FinishedAt.UTC()).SetDurationMs(duration).SetToolCalls(toolCalls).
			SetFinishReason(turn.FinishReason).SetInputTokens(turn.Usage.InputTokens).SetOutputTokens(turn.Usage.OutputTokens).
			SetTotalTokens(turn.Usage.TotalTokens).SetCachedTokens(turn.Usage.CacheHitTokens).SetReasoningTokens(turn.Usage.ReasoningTokens).
			SetNillableContextTokens(turn.ContextTokens).SetContextWindow(turn.ContextWindow).
			SetCompactionCount(turn.CompactionCount).SetMessages(turn.Messages).ClearContextMessages()
		if turn.ContextMessages != nil {
			update.SetContextMessages(turn.ContextMessages)
		}
		if _, err := update.Save(ctx); err != nil {
			return err
		}
		if _, err := client.KaguyaChatBlock.Delete().Where(kaguyachatblock.TurnIDEQ(turnID)).Exec(ctx); err != nil {
			return err
		}
	}
	// block 单独逐条 INSERT 会长时间占用 SQLite 唯一连接；按批合并写入，
	// 保持同一事务的原子性，同时避免单条语句变量数过大。
	const blockBatchSize = 200
	for start := 0; start < len(turn.Blocks); start += blockBatchSize {
		end := min(start+blockBatchSize, len(turn.Blocks))
		builders := make([]*ent.KaguyaChatBlockCreate, 0, end-start)
		for _, b := range turn.Blocks[start:end] {
			create := client.KaguyaChatBlock.Create().SetTurnID(turnID).SetSequence(b.Sequence).SetType(kaguyachatblock.Type(b.Type)).
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
			builders = append(builders, create)
		}
		if err := client.KaguyaChatBlock.CreateBulk(builders...).Exec(ctx); err != nil {
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
	_, release, err := acquireConversation(id)
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
	_, release, err := acquireConversation(id)
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
	if _, err := s.ConversationDetail(ctx, id); err != nil {
		return nil, err
	}
	// 展示层包含进行中/中断的轮次，让用户能看到已产生的内容；续聊上下文与用量只信任 completed。
	q := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(id))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	totalPages := int((int64(total) + int64(req.Limit) - 1) / int64(req.Limit))
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
		if f != kaguyachatturn.FieldMessages && f != kaguyachatturn.FieldContextMessages {
			fields = append(fields, f)
		}
	}
	// Page numbers already have an exact turn-index range. Cursor mode uses
	// one extra metadata row to detect older history, without loading its blocks.
	queryLimit := req.Limit
	if page == 0 {
		queryLimit++
	}
	rows, err := q.Select(fields...).Order(kaguyachatturn.ByTurnIndex(sql.OrderDesc())).Limit(queryLimit).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtochat.TurnListResp{Items: make([]dtochat.StoredTurn, 0), HasMore: len(rows) > req.Limit,
		Total: int64(total), Page: page, PageSize: req.Limit, TotalPages: totalPages}
	if resp.HasMore {
		rows = rows[:req.Limit]
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	blocksByTurn := make(map[string][]*ent.KaguyaChatBlock)
	if len(ids) > 0 {
		blocksQuery := db.EntClient.KaguyaChatBlock.Query().Where(kaguyachatblock.TurnIDIn(ids...)).Order(kaguyachatblock.BySequence())
		if req.Compact {
			// Do not even read large folded payloads from the database.
			blockFields := make([]string, 0, len(kaguyachatblock.Columns))
			for _, field := range kaguyachatblock.Columns {
				if field != kaguyachatblock.FieldText && field != kaguyachatblock.FieldInput && field != kaguyachatblock.FieldOutput && field != kaguyachatblock.FieldErrorMessage {
					blockFields = append(blockFields, field)
				}
			}
			blocksQuery.Select(blockFields...)
		}
		blocks, err := blocksQuery.All(ctx)
		if err != nil {
			return nil, err
		}
		if req.Compact {
			texts, err := db.EntClient.KaguyaChatBlock.Query().Where(kaguyachatblock.TurnIDIn(ids...), kaguyachatblock.TypeEQ(kaguyachatblock.TypeText)).Select(kaguyachatblock.FieldID, kaguyachatblock.FieldText).All(ctx)
			if err != nil {
				return nil, err
			}
			textByID := make(map[string]string, len(texts))
			for _, block := range texts {
				textByID[block.ID] = block.Text
			}
			for _, block := range blocks {
				block.Text = textByID[block.ID]
			}
		}
		for _, block := range blocks {
			blocksByTurn[block.TurnID] = append(blocksByTurn[block.TurnID], block)
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		turn := dtochat.StoredTurn{TurnIndex: row.TurnIndex, UserContent: row.UserContent, ProviderName: row.ProviderName,
			ModelID: row.ModelID, ModelName: row.ModelName, APIProtocol: row.APIProtocol, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
			DurationMS: row.DurationMs, ToolCalls: row.ToolCalls, FinishReason: row.FinishReason, Status: string(row.Status),
			Usage:  dtochat.Usage{InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens), TotalTokens: int(row.TotalTokens), CachedTokens: int(row.CachedTokens), ReasoningTokens: int(row.ReasoningTokens)},
			Blocks: make([]dtochat.StoredBlock, 0, len(blocksByTurn[row.ID]))}
		for _, b := range blocksByTurn[row.ID] {
			block, err := storedBlock(b)
			if err != nil {
				return nil, err
			}
			if req.Compact && b.Type != kaguyachatblock.TypeText {
				block.DetailsDeferred = true
				// 只有真正产生了结果（结束顺序推进）的工具块才可展开；
				// 进行中/中断的工具调用结束顺序与开始相同，避免无结果时请求详情。
				block.HasOutput = b.Type == kaguyachatblock.TypeToolCall && b.EndOrder > b.StartOrder
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

func storedBlock(b *ent.KaguyaChatBlock) (dtochat.StoredBlock, error) {
	block := dtochat.StoredBlock{Sequence: b.Sequence, Type: dtochat.BlockType(b.Type), Text: b.Text, ToolCallID: b.ToolCallID,
		ToolName: b.ToolName, Input: b.Input, ProviderExecuted: b.ProviderExecuted, IsError: b.IsError, ErrorMessage: b.ErrorMessage,
		StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, StartOrder: b.StartOrder, EndOrder: b.EndOrder}
	if len(b.Output) > 0 {
		if err := json.Unmarshal(b.Output, &block.Output); err != nil {
			return block, fmt.Errorf("decode stored tool output: %w", err)
		}
	}
	return block, nil
}

func (s *AgentSvc) ConversationBlock(ctx context.Context, req *dtochat.BlockDetailReq) (*dtochat.StoredBlock, error) {
	if req.TurnIndex < 1 || req.Sequence < 1 {
		return nil, ErrConversationUpdate
	}
	row, err := db.EntClient.KaguyaChatBlock.Query().Where(kaguyachatblock.SequenceEQ(req.Sequence),
		kaguyachatblock.HasTurnWith(kaguyachatturn.ConversationIDEQ(req.ID), kaguyachatturn.TurnIndexEQ(req.TurnIndex),
			kaguyachatturn.HasConversationWith(kaguyaconversation.DeletedAtIsNil()))).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	block, err := storedBlock(row)
	return &block, err
}
