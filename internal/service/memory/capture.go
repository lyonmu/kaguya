package memory

import (
	"context"
	"fmt"

	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryevidence"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproject"
)

// CaptureInput 是 completed 轮次的轻量来源引用；捕获不读大量历史、不调用模型、
// 不建全文索引、不做页面合并。
type CaptureInput struct {
	ConversationID string
	TurnID         string
	PolicyEpoch    int64
}

// CaptureCompletedTx 在 saveCompletedTurn 的既有事务内写入来源待处理记录，
// 来源表兼任持久化 outbox：数据库已成功而 SSE 发送失败时 Memory 仍会处理。
// 捕获函数从事务中的 Conversation 查询真实项目归属，不信任请求字段；
// 未完成轮次不自动捕获。唯一约束防止重复入队。
func CaptureCompletedTx(ctx context.Context, client *ent.Client, in CaptureInput) error {
	conv, err := client.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(in.ConversationID)).
		Select(kaguyaconversation.FieldProjectID, kaguyaconversation.FieldMemoryMode, kaguyaconversation.FieldDeletedAt).
		Only(ctx)
	if err != nil {
		return err
	}
	if conv.DeletedAt != nil || MemoryMode(conv.MemoryMode) == ModeOff || MemoryMode(conv.MemoryMode) == ModeReadOnly {
		return nil
	}
	projectID := ""
	if conv.ProjectID != nil {
		projectID = *conv.ProjectID
	}
	if projectID != "" {
		// 项目删除后停止摄取；历史项目记忆不重新归入 personal/shared。
		active, err := client.KaguyaProject.Query().
			Where(kaguyaproject.IDEQ(projectID), kaguyaproject.DeletedAtIsNil()).Exist(ctx)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
	}
	// 未完成轮次不自动捕获：running/failed/canceled/interrupted 都不入队。
	turn, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.IDEQ(in.TurnID)).
		Select(kaguyachatturn.FieldStatus).Only(ctx)
	if err != nil {
		return err
	}
	if turn.Status != kaguyachatturn.StatusCompleted {
		return nil
	}
	// 轻量 Outbox：只写后续处理真正需要的稳定标识与范围；投影、内容哈希与
	// 脱敏全部留给 Worker 异步推导，捕获事务不读来源投影。
	return client.KaguyaMemorySource.Create().
		SetSourceKey(fmt.Sprintf("turn:%s:projection-v%d", in.TurnID, ProjectionVersion)).
		SetKind(kaguyamemorysource.KindTurn).
		SetScopeKey(ScopeKey(projectID)).
		SetConversationID(in.ConversationID).
		SetTurnID(in.TurnID).
		SetProjectionVersion(ProjectionVersion).
		SetState(kaguyamemorysource.StatePending).
		SetCapturedAt(nowTime()).
		SetPolicyEpoch(in.PolicyEpoch).
		OnConflictColumns(kaguyamemorysource.FieldSourceKey).
		Ignore().
		Exec(ctx)
}

// EnsureTurnSourceTx 为人工保存的记忆建立（或复用）轮次来源引用，
// 使“保存为记忆”的来源跳转与证据校验有真实投影可依。已在自动捕获中入队时
// 沿用原记录；不改变其待处理状态。
func EnsureTurnSourceTx(ctx context.Context, client *ent.Client, conversationID, turnID string) (*ent.KaguyaMemorySource, error) {
	key := fmt.Sprintf("turn:%s:projection-v%d", turnID, ProjectionVersion)
	if existing, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx); err == nil {
		return existing, nil
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	conv, err := client.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(conversationID)).
		Select(kaguyaconversation.FieldProjectID, kaguyaconversation.FieldDeletedAt).Only(ctx)
	if err != nil {
		return nil, err
	}
	if conv.DeletedAt != nil {
		return nil, ErrConversationGone
	}
	projectID := ""
	if conv.ProjectID != nil {
		projectID = *conv.ProjectID
	}
	// 只校验轮次确实属于该会话；投影与哈希在需要时异步推导。
	if _, err := client.KaguyaChatTurn.Query().
		Where(kaguyachatturn.IDEQ(turnID), kaguyachatturn.ConversationIDEQ(conversationID)).
		Select(kaguyachatturn.FieldID).Only(ctx); err != nil {
		return nil, err
	}
	// 人工保存是用户明确动作，来源直接标记已处理，不进入自动编译循环。
	row, err := client.KaguyaMemorySource.Create().
		SetSourceKey(key).
		SetKind(kaguyamemorysource.KindTurn).
		SetScopeKey(ScopeKey(projectID)).
		SetConversationID(conversationID).
		SetTurnID(turnID).
		SetProjectionVersion(ProjectionVersion).
		SetState(kaguyamemorysource.StateProcessed).
		SetCapturedAt(nowTime()).
		SetPolicyEpoch(0).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发捕获同一来源：唯一约束防止重复入队，沿用已有记录。
		return client.KaguyaMemorySource.Query().
			Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx)
	}
	return row, err
}

// EnsureNoteSourceTx 为人工页面维护笔记来源；笔记正文即用户陈述投影。
// content_hash 随编辑更新，来源行与页面同事务保存。
func EnsureNoteSourceTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage, policyEpoch int64) (*ent.KaguyaMemorySource, error) {
	key := "note:" + page.ID
	hash := ProjectionHash(BuildNoteSegments(page.Body))
	existing, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		row, err := client.KaguyaMemorySource.Create().
			SetSourceKey(key).
			SetKind(kaguyamemorysource.KindNote).
			SetScopeKey(page.ScopeKey).
			SetProjectionVersion(ProjectionVersion).
			SetContentHash(hash).
			SetState(kaguyamemorysource.StateProcessed).
			SetCapturedAt(nowTime()).
			SetPolicyEpoch(policyEpoch).
			Save(ctx)
		if ent.IsConstraintError(err) {
			return client.KaguyaMemorySource.Query().
				Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx)
		}
		return row, err
	}
	if err != nil {
		return nil, err
	}
	if existing.ContentHash != hash {
		if _, err := client.KaguyaMemorySource.UpdateOneID(existing.ID).
			SetContentHash(hash).SetScopeKey(page.ScopeKey).Save(ctx); err != nil {
			return nil, err
		}
		existing.ContentHash = hash
	}
	return existing, nil
}

// OnConversationDeletedTx 在会话删除事务内撤销该会话来源的可用性并取消
// 待处理来源/作业；依赖这些来源的活动页面保守标 stale 移出自动召回。
// purge 为真时进一步删除来源及其派生记忆内容；用户明确保存的独立笔记
// 仅在 purge 时一并清理。绝不把来源改写到其他作用域。
func OnConversationDeletedTx(ctx context.Context, client *ent.Client, conversationID string, purge bool) error {
	sources, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.ConversationIDEQ(conversationID),
			kaguyamemorysource.StateNEQ(kaguyamemorysource.StateExcluded)).
		All(ctx)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}
	ids := make([]string, 0, len(sources))
	for _, src := range sources {
		ids = append(ids, src.ID)
	}
	// 待处理/进行中的作业取消：来源已失效，不能继续编译。
	if err := client.KaguyaMemoryJob.Update().
		Where(kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusPending, kaguyamemoryjob.StatusRunning,
			kaguyamemoryjob.StatusRetryWait, kaguyamemoryjob.StatusBlocked)).
		Where(kaguyamemoryjob.ConversationIDEQ(conversationID)).
		SetStatus(kaguyamemoryjob.StatusCanceled).SetErrorCode("source_excluded").
		SetErrorSummary("conversation deleted").SetFinishedAt(nowTime()).Exec(ctx); err != nil {
		return err
	}
	affected, err := pagesSupportedBy(ctx, client, ids)
	if err != nil {
		return err
	}
	if purge {
		if err := purgeSourceEvidenceTx(ctx, client, ids); err != nil {
			return err
		}
		if _, err := client.KaguyaMemorySource.Delete().
			Where(kaguyamemorysource.IDIn(ids...)).Exec(ctx); err != nil {
			return err
		}
	} else {
		if err := client.KaguyaMemorySource.Update().
			Where(kaguyamemorysource.IDIn(ids...)).
			SetState(kaguyamemorysource.StateExcluded).Exec(ctx); err != nil {
			return err
		}
	}
	for _, pageID := range affected {
		page, err := client.KaguyaMemoryPage.Get(ctx, pageID)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if page.Status == kaguyamemorypage.StatusDeleted {
			continue
		}
		if purge {
			// 派生记忆随来源清理；还剩独立证据的页面只标失效待核验。
			remaining, err := pageHasEvidenceTx(ctx, client, pageID)
			if err != nil {
				return err
			}
			if !remaining {
				if err := forgetPageTx(ctx, client, page, "system", "source purged"); err != nil {
					return err
				}
				continue
			}
		}
		if err := markPageStaleTx(ctx, client, page, "system", "supporting source excluded"); err != nil {
			return err
		}
	}
	return BumpPolicyEpoch(ctx, client)
}

// OnProjectDeletedTx 项目删除：范围保持原项目身份且不再召回，
// 待处理任务取消，绝不改写为 personal/shared。
func OnProjectDeletedTx(ctx context.Context, client *ent.Client, projectID string) error {
	scope := ScopeKey(projectID)
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.ScopeKeyEQ(scope),
			kaguyamemorysource.StateIn(kaguyamemorysource.StatePending, kaguyamemorysource.StateClaimed,
				kaguyamemorysource.StateFailed)).
		SetState(kaguyamemorysource.StateExcluded).Exec(ctx); err != nil {
		return err
	}
	if err := client.KaguyaMemoryJob.Update().
		Where(kaguyamemoryjob.ScopeKeyEQ(scope),
			kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusPending, kaguyamemoryjob.StatusRunning,
				kaguyamemoryjob.StatusRetryWait, kaguyamemoryjob.StatusBlocked)).
		SetStatus(kaguyamemoryjob.StatusCanceled).SetErrorCode("project_deleted").
		SetErrorSummary("project deleted").SetFinishedAt(nowTime()).Exec(ctx); err != nil {
		return err
	}
	return BumpPolicyEpoch(ctx, client)
}

// OnProjectPathChangedTx 项目换绑可能意味着代码对象更换：
// 提升该范围的失效版本，旧页面标待核验，不悄悄当成同一个代码状态。
func OnProjectPathChangedTx(ctx context.Context, client *ent.Client, projectID string) error {
	scope := ScopeKey(projectID)
	pages, err := client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.ScopeKeyEQ(scope),
			kaguyamemorypage.StatusIn(kaguyamemorypage.StatusActive, kaguyamemorypage.StatusConflicted, kaguyamemorypage.StatusStale)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, page := range pages {
		if err := markPageStaleTx(ctx, client, page, "system", "project path changed"); err != nil {
			return err
		}
	}
	return BumpPolicyEpoch(ctx, client)
}

// pageHasEvidenceTx 判断页面是否仍有任何证据引用。
func pageHasEvidenceTx(ctx context.Context, client *ent.Client, pageID string) (bool, error) {
	revisionIDs, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(pageID)).
		IDs(ctx)
	if err != nil || len(revisionIDs) == 0 {
		return false, err
	}
	return client.KaguyaMemoryEvidence.Query().
		Where(kaguyamemoryevidence.RevisionIDIn(revisionIDs...)).Exist(ctx)
}

// pagesSupportedBy 返回修订证据引用了指定来源的页面 ID。
func pagesSupportedBy(ctx context.Context, client *ent.Client, sourceIDs []string) ([]string, error) {
	evidence, err := client.KaguyaMemoryEvidence.Query().
		Where(kaguyamemoryevidence.SourceIDIn(sourceIDs...)).
		Select(kaguyamemoryevidence.FieldRevisionID).All(ctx)
	if err != nil {
		return nil, err
	}
	revisionIDs := make([]string, 0, len(evidence))
	for _, row := range evidence {
		revisionIDs = append(revisionIDs, row.RevisionID)
	}
	if len(revisionIDs) == 0 {
		return nil, nil
	}
	revisions, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.IDIn(revisionIDs...)).
		Select(kaguyamemoryrevision.FieldID, kaguyamemoryrevision.FieldPageID).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	pages := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		if !seen[revision.PageID] {
			seen[revision.PageID] = true
			pages = append(pages, revision.PageID)
		}
	}
	return pages, nil
}

// purgeSourceEvidenceTx 清除来源相关的证据摘录，避免删除后仍可通过读取绕过。
func purgeSourceEvidenceTx(ctx context.Context, client *ent.Client, sourceIDs []string) error {
	_, err := client.KaguyaMemoryEvidence.Delete().
		Where(kaguyamemoryevidence.SourceIDIn(sourceIDs...)).Exec(ctx)
	return err
}
