package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryevidence"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorylink"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysearchdoc"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// ErrStaleLease 表示发布时租约已易主或作业不再由本 attempt 控制；
// 超时 worker 不能在新 worker 接管后提交旧结果。
var ErrStaleLease = errors.New("memory job lease is stale")

// tombstoneTitle 是删除记忆后保留的最小占位标题；正文、修订与证据已清除。
const tombstoneTitle = "（已删除）"

// PublishResult 是一次发布事务的结果摘要。
type PublishResult struct {
	Published int  // 已发布的 create/update 页面数
	Review    int  // 转入待审的提案数
	Noop      int  // 无长期价值的变更数
	Dropped   int  // 因删除/锁定等原因放弃的变更数
	NeedsJob  bool // Job 应进入 needs_review
}

// claimActor 是修订的产生者。
type claimActor string

const (
	actorUser      claimActor = "user"
	actorTaskModel claimActor = "task_model"
	actorSystem    claimActor = "system"
)

// Publish 在单个短事务内重新检查租约、来源、权限与页面版本后原子发布：
// 页面 + 修订 + 证据 + 关系 + 搜索投影 + Job/Source 状态一起提交，
// 页面改动和任务完成之间崩溃时要么全部提交，要么全部回滚。
// 覆盖范围推进到本次投影包含的分段；未覆盖分段回到待处理。
func (s *Service) Publish(ctx context.Context, job *ent.KaguyaMemoryJob, plan *dtomemory.PatchPlan,
	projections []*SourceProjection) (PublishResult, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	release, err := checkLeaseAndSources(ctx, client, job)
	if err != nil {
		return PublishResult{}, err
	}
	if release != nil {
		// 策略版本已失效：保守丢弃在途结果，来源回到待处理后由新任务重新校验。
		if err := release(ctx); err != nil {
			return PublishResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return PublishResult{}, err
		}
		return PublishResult{}, ErrStaleLease
	}

	candidates, err := candidatePagesTx(ctx, client, job.ScopeKey, plan)
	if err != nil {
		return PublishResult{}, err
	}
	result := PublishResult{}
	review := make([]dtomemory.PagePatch, 0)
	for i := range plan.Changes {
		change := &plan.Changes[i]
		outcome, err := applyChangeTx(ctx, client, job, change, candidates, applyOptions{actor: actorTaskModel})
		if err != nil {
			return PublishResult{}, err
		}
		switch outcome {
		case changePublished:
			result.Published++
		case changeNoop:
			result.Noop++
		case changeDropped:
			result.Dropped++
		case changeReview:
			review = append(review, *change)
		}
	}

	if len(review) > 0 {
		result.NeedsJob = true
		result.Review = len(review)
		if err := saveReviewProposalTx(ctx, client, job, review); err != nil {
			return PublishResult{}, err
		}
	}
	if err := completeSourcesTx(ctx, client, job, projections, result); err != nil {
		return PublishResult{}, err
	}
	if err := finishJobTx(ctx, client, job, result, ""); err != nil {
		return PublishResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

type changeOutcome int

const (
	changePublished changeOutcome = iota
	changeNoop
	changeDropped
	changeReview
)

// applyOptions 区分自动发布与用户批准：批准是用户明确动作，可越过锁定挑战与
// 版本竞争，以当前页面版本发布新修订。
type applyOptions struct {
	actor    claimActor
	approved bool
}

// applyChangeTx 应用单个变更；update 使用 WHERE id=? AND version=? 乐观并发控制。
// 用户编辑或忘记发生在模型运行中时，旧提案不能覆盖或复活内容。
func applyChangeTx(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob,
	change *dtomemory.PagePatch, candidates map[string]*ent.KaguyaMemoryPage, opts applyOptions) (changeOutcome, error) {
	if opts.actor == "" {
		opts.actor = actorTaskModel
	}
	if opts.approved && change.Action == "conflict" {
		// 用户批准冲突提案：按当前版本发布合并结果。
		change.Action = "update"
	}
	switch change.Action {
	case "noop":
		return changeNoop, nil
	case "create":
		existing, err := client.KaguyaMemoryPage.Query().
			Where(kaguyamemorypage.ScopeKeyEQ(job.ScopeKey), kaguyamemorypage.CanonicalKeyEQ(change.CanonicalKey)).
			Only(ctx)
		if err == nil {
			if existing.Status == kaguyamemorypage.StatusDeleted {
				// 删除后的 tombstone 阻止自动复活。
				return changeDropped, nil
			}
			if opts.approved {
				change.Action, change.PageID, change.BaseVersion = "update", existing.ID, existing.Version
				candidates[existing.ID] = existing
				return applyChangeTx(ctx, client, job, change, candidates, opts)
			}
			// 模型没有按候选集更新已有页面：作为待审提案，不强制覆盖。
			return changeReview, nil
		}
		if !ent.IsNotFound(err) {
			return changeDropped, err
		}
		if !strongEvidence(change) && !opts.approved {
			return changeReview, nil
		}
		status := publishedStatus(change)
		if opts.approved {
			status = kaguyamemorypage.StatusActive
		}
		page, err := client.KaguyaMemoryPage.Create().
			SetScopeKey(job.ScopeKey).SetCanonicalKey(change.CanonicalKey).
			SetKind(kaguyamemorypage.Kind(change.Kind)).
			SetTitle(change.Title).SetSummary(change.Summary).SetBody(change.Body).
			SetAliases(defaultAliases(change.Aliases)).
			SetStatus(status).SetVersion(1).
			Save(ctx)
		if err != nil {
			return changeDropped, err
		}
		if err := saveRevisionTx(ctx, client, page, change, opts.actor, job.ID); err != nil {
			return changeDropped, err
		}
		if err := replaceRelatedLinksTx(ctx, client, page.ID, change.RelatedIDs); err != nil {
			return changeDropped, err
		}
		if err := writeSearchDocTx(ctx, client, page); err != nil {
			return changeDropped, err
		}
		return changePublished, nil
	case "update", "conflict":
		candidate := candidates[change.PageID]
		if candidate == nil {
			return changeDropped, nil
		}
		page, err := client.KaguyaMemoryPage.Get(ctx, change.PageID)
		if ent.IsNotFound(err) {
			// 已被忘记的页面不能被在途提案复活。
			return changeDropped, nil
		}
		if err != nil {
			return changeDropped, err
		}
		if page.Status == kaguyamemorypage.StatusDeleted {
			return changeDropped, nil
		}
		if page.Version != change.BaseVersion {
			// 冻结候选之后的用户编辑不能被旧冲突提案下线。
			return changeReview, nil
		}
		if change.Action == "conflict" {
			// 强证据（用户陈述/工具观察）且未锁定时，同事务把原页面标为 conflicted、
			// 递增版本并移出自动召回，但保留原正文；弱助手猜测不能强制下线可靠页面。
			if strongEvidence(change) && !page.UserLocked {
				updated, err := setPageStatusTx(ctx, client, page, kaguyamemorypage.StatusConflicted, actorSystem, job.ID, "conflicting evidence")
				if err != nil {
					return changeDropped, err
				}
				page = updated
			}
			// 冲突提案进待审，由用户决定是否合并。
			return changeReview, nil
		}
		if page.UserLocked && !opts.approved {
			// 人工锁定页面被新资料挑战：待审修订，不自动改正文。
			return changeReview, nil
		}
		status := page.Status
		if strongEvidence(change) || opts.approved {
			status = kaguyamemorypage.StatusActive
		} else if status == kaguyamemorypage.StatusActive {
			// 助手推断不得自动替换已确认页面，交给用户审阅。
			return changeReview, nil
		}
		updated, err := client.KaguyaMemoryPage.UpdateOneID(page.ID).
			SetKind(kaguyamemorypage.Kind(change.Kind)).
			SetTitle(change.Title).SetSummary(change.Summary).SetBody(change.Body).
			SetAliases(defaultAliases(change.Aliases)).
			SetStatus(status).SetVersion(page.Version + 1).
			Where(kaguyamemorypage.VersionEQ(page.Version)).
			Save(ctx)
		if ent.IsNotFound(err) {
			// 并发版本条件未命中：转待审，不能强制覆盖。
			return changeReview, nil
		}
		if err != nil {
			return changeDropped, err
		}
		if err := saveRevisionTx(ctx, client, updated, change, opts.actor, job.ID); err != nil {
			return changeDropped, err
		}
		if err := replaceRelatedLinksTx(ctx, client, updated.ID, change.RelatedIDs); err != nil {
			return changeDropped, err
		}
		if err := writeSearchDocTx(ctx, client, updated); err != nil {
			return changeDropped, err
		}
		return changePublished, nil
	}
	return changeDropped, nil
}

// publishedStatus 决定编译结果的初始状态：没有可核验证据的敏感/推测内容
// 保留 proposed，不能自动激活。
func publishedStatus(change *dtomemory.PagePatch) kaguyamemorypage.Status {
	if strongEvidence(change) {
		return kaguyamemorypage.StatusActive
	}
	return kaguyamemorypage.StatusProposed
}

// strongEvidence 要求每条主张都有强支持证据；不能用一条用户陈述
// 为同页的助手推断背书，反驳证据也不能当作支持。
func strongEvidence(change *dtomemory.PagePatch) bool {
	if len(change.Claims) == 0 {
		return false
	}
	for _, claim := range change.Claims {
		supported := false
		for _, ev := range claim.Evidence {
			if ev.Quote != "" && ev.Relation != "refute" {
				switch claim.Basis {
				case "user_statement", "tool_observation", "document_statement":
					supported = true
				}
			}
		}
		if !supported {
			return false
		}
	}
	return true
}

func defaultAliases(aliases []string) []string {
	if aliases == nil {
		return []string{}
	}
	return aliases
}

// setPageStatusTx 递增版本并记录状态迁移修订，随后同步搜索投影。
func setPageStatusTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage,
	status kaguyamemorypage.Status, actor claimActor, jobID, reason string) (*ent.KaguyaMemoryPage, error) {
	updated, err := client.KaguyaMemoryPage.UpdateOneID(page.ID).
		SetStatus(status).SetVersion(page.Version + 1).
		Where(kaguyamemorypage.VersionEQ(page.Version)).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := &dtomemory.PagePatch{
		Kind: string(page.Kind), Title: page.Title, Summary: page.Summary, Body: page.Body,
		Aliases: page.Aliases, Reason: reason,
	}
	snapshot.Claims, err = currentClaimsTx(ctx, client, page)
	if err != nil {
		return nil, err
	}
	if err := saveRevisionTx(ctx, client, updated, snapshot, actor, jobID); err != nil {
		return nil, err
	}
	if err := writeSearchDocTx(ctx, client, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// currentClaimsTx preserves provenance when only metadata or status changes.
func currentClaimsTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage) ([]dtomemory.ClaimPatch, error) {
	data, err := loadCandidatePageData(ctx, client, page)
	if err != nil {
		return nil, err
	}
	claims := make([]dtomemory.ClaimPatch, 0, len(data.claims))
	for _, claim := range data.claims {
		patch := dtomemory.ClaimPatch{Key: claim.Key, Statement: claim.Statement, Basis: claim.Basis}
		for _, row := range data.rows {
			if row.ClaimKey == claim.Key {
				patch.Evidence = append(patch.Evidence, dtomemory.ClaimEvidence{
					SourceID: row.SourceID, PartKey: row.PartKey, Quote: row.Quote, Relation: string(row.Relation),
				})
			}
		}
		claims = append(claims, patch)
	}
	return claims, nil
}

// markPageStaleTx 把页面标为待核验并移出自动召回。
func markPageStaleTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage, actor claimActor, reason string) error {
	if page.Status == kaguyamemorypage.StatusDeleted {
		return nil
	}
	_, err := setPageStatusTx(ctx, client, page, kaguyamemorypage.StatusStale, actor, "", reason)
	return err
}

// forgetPageTx 执行分层删除中的“删除记忆”：撤出索引，清除正文、修订、证据摘录
// 与关系，保留最小 tombstone 占位防止待处理作业复活。
func forgetPageTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage, actor claimActor, reason string) error {
	if _, err := client.KaguyaMemorySource.Delete().Where(kaguyamemorysource.Or(
		kaguyamemorysource.SourceKeyEQ("note:"+page.ID), kaguyamemorysource.SourceKeyHasPrefix("note:"+page.ID+":v"),
	)).Exec(ctx); err != nil {
		return err
	}
	revisionIDs, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(page.ID)).
		IDs(ctx)
	if err != nil {
		return err
	}
	if len(revisionIDs) > 0 {
		if _, err := client.KaguyaMemoryEvidence.Delete().
			Where(kaguyamemoryevidence.RevisionIDIn(revisionIDs...)).Exec(ctx); err != nil {
			return err
		}
		if _, err := client.KaguyaMemoryRevision.Delete().
			Where(kaguyamemoryrevision.IDIn(revisionIDs...)).Exec(ctx); err != nil {
			return err
		}
	}
	if _, err := client.KaguyaMemoryLink.Delete().
		Where(kaguyamemorylink.FromPageIDEQ(page.ID)).Exec(ctx); err != nil {
		return err
	}
	if err := deleteSearchDocTx(ctx, client, page.ID); err != nil {
		return err
	}
	return client.KaguyaMemoryPage.UpdateOneID(page.ID).
		SetTitle(tombstoneTitle).SetSummary("").SetBody("").
		SetAliases([]string{}).SetStatus(kaguyamemorypage.StatusDeleted).
		SetPinned(false).SetUserLocked(false).ClearExpiresAt().
		SetVersion(page.Version + 1).Exec(ctx)
}

// saveRevisionTx 保存完整页面快照修订；Revision 只存已发布状态。
func saveRevisionTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage,
	change *dtomemory.PagePatch, actor claimActor, jobID string) error {
	claims := make([]dtomemory.MemoryClaim, 0, len(change.Claims))
	for _, claim := range change.Claims {
		claims = append(claims, dtomemory.MemoryClaim{Key: claim.Key, Statement: claim.Statement, Basis: claim.Basis})
	}
	create := client.KaguyaMemoryRevision.Create().
		SetPageID(page.ID).SetVersion(page.Version).
		SetCanonicalKey(page.CanonicalKey).SetKind(kaguyamemoryrevision.Kind(page.Kind)).
		SetTitle(page.Title).SetSummary(page.Summary).SetBody(page.Body).
		SetAliases(defaultAliases(page.Aliases)).SetStatus(kaguyamemoryrevision.Status(page.Status)).
		SetPinned(page.Pinned).SetUserLocked(page.UserLocked).
		SetActor(kaguyamemoryrevision.Actor(actor)).
		SetJobID(jobID).SetReason(change.Reason)
	if page.ExpiresAt != nil {
		create.SetExpiresAt(*page.ExpiresAt)
	}
	revision, err := create.SetClaims(claims).Save(ctx)
	if err != nil {
		return err
	}
	if len(change.Claims) == 0 {
		return nil
	}
	builders := make([]*ent.KaguyaMemoryEvidenceCreate, 0, len(change.Claims)*2)
	for _, claim := range change.Claims {
		for _, ev := range claim.Evidence {
			relation := kaguyamemoryevidence.RelationSupport
			if ev.Relation == "refute" {
				relation = kaguyamemoryevidence.RelationRefute
			}
			builders = append(builders, client.KaguyaMemoryEvidence.Create().
				SetRevisionID(revision.ID).SetClaimKey(claim.Key).
				SetSourceID(ev.SourceID).SetPartKey(ev.PartKey).
				SetQuote(ev.Quote).SetQuoteHash(QuoteHash(ev.Quote)).
				SetRelation(relation).SetBasis(kaguyamemoryevidence.Basis(claim.Basis)))
		}
	}
	return client.KaguyaMemoryEvidence.CreateBulk(builders...).Exec(ctx)
}

// replaceRelatedLinksTx 以本次计划替换 related 关系；supersedes 关系不受影响，
// 由 replaceSupersedesLinksTx 单独维护。
func replaceRelatedLinksTx(ctx context.Context, client *ent.Client, pageID string, relatedIDs []string) error {
	if _, err := client.KaguyaMemoryLink.Delete().
		Where(kaguyamemorylink.FromPageIDEQ(pageID), kaguyamemorylink.RelationEQ(kaguyamemorylink.RelationRelated)).
		Exec(ctx); err != nil {
		return err
	}
	return createLinksTx(ctx, client, pageID, relatedIDs, kaguyamemorylink.RelationRelated)
}

// replaceSupersedesLinksTx 维护显式替代关系（由用户或后续编译显式建立）。
func replaceSupersedesLinksTx(ctx context.Context, client *ent.Client, pageID string, supersedesIDs []string) error {
	if _, err := client.KaguyaMemoryLink.Delete().
		Where(kaguyamemorylink.FromPageIDEQ(pageID), kaguyamemorylink.RelationEQ(kaguyamemorylink.RelationSupersedes)).
		Exec(ctx); err != nil {
		return err
	}
	return createLinksTx(ctx, client, pageID, supersedesIDs, kaguyamemorylink.RelationSupersedes)
}

func createLinksTx(ctx context.Context, client *ent.Client, pageID string, targets []string, relation kaguyamemorylink.Relation) error {
	if len(targets) == 0 {
		return nil
	}
	builders := make([]*ent.KaguyaMemoryLinkCreate, 0, len(targets))
	for _, to := range targets {
		builders = append(builders, client.KaguyaMemoryLink.Create().
			SetFromPageID(pageID).SetToPageID(to).SetRelation(relation))
	}
	return client.KaguyaMemoryLink.CreateBulk(builders...).Exec(ctx)
}

// writeSearchDocTx 维护搜索投影：仅 active 页面保留在自动召回投影中；
// 使用普通 INSERT/UPDATE 修改投影表，FTS 由 triggers 同步。
func writeSearchDocTx(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage) error {
	if page.Status != kaguyamemorypage.StatusActive {
		return deleteSearchDocTx(ctx, client, page.ID)
	}
	fields := searchDocFields(page)
	existing, err := client.KaguyaMemorySearchDoc.Query().
		Where(kaguyamemorysearchdoc.PageIDEQ(page.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return client.KaguyaMemorySearchDoc.Create().
			SetPageID(page.ID).SetPageVersion(page.Version).
			SetNormalizerVersion(NormalizerVersion).
			SetTitleTerms(fields.title).SetAliasTerms(fields.alias).
			SetSummaryTerms(fields.summary).SetBodyTerms(fields.body).
			Exec(ctx)
	}
	if err != nil {
		return err
	}
	return client.KaguyaMemorySearchDoc.UpdateOneID(existing.ID).
		SetPageVersion(page.Version).SetNormalizerVersion(NormalizerVersion).
		SetTitleTerms(fields.title).SetAliasTerms(fields.alias).
		SetSummaryTerms(fields.summary).SetBodyTerms(fields.body).
		Exec(ctx)
}

func deleteSearchDocTx(ctx context.Context, client *ent.Client, pageID string) error {
	_, err := client.KaguyaMemorySearchDoc.Delete().
		Where(kaguyamemorysearchdoc.PageIDEQ(pageID)).Exec(ctx)
	return err
}

type searchDocFieldText struct{ title, alias, summary, body string }

func searchDocFields(page *ent.KaguyaMemoryPage) searchDocFieldText {
	return searchDocFieldText{
		title:   NormalizeFTS(page.Title),
		alias:   NormalizeFTS(joinAliases(page.Aliases)),
		summary: NormalizeFTS(page.Summary),
		body:    NormalizeFTS(page.Body),
	}
}

func joinAliases(aliases []string) string {
	out := ""
	for i, alias := range aliases {
		if i > 0 {
			out += " "
		}
		out += alias
	}
	return out
}

// QuoteHash 是证据摘录的稳定哈希，发布校验 quote 一致性时使用。
func QuoteHash(quote string) string {
	return ProjectionHash([]Segment{{Text: quote}})
}

// checkLeaseAndSources 在发布事务内复核租约、来源与策略版本。
// 返回非 nil release 表示策略版本失效，需要保守放弃结果。
func checkLeaseAndSources(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob) (func(context.Context) error, error) {
	row, err := client.KaguyaMemoryJob.Get(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	// 租约 token 相等即本 attempt 仍持有作业；每次尝试换新 token 避免 ABA。
	if row.Status != kaguyamemoryjob.StatusRunning || row.LeaseToken == "" || row.LeaseToken != job.LeaseToken {
		return nil, ErrStaleLease
	}
	sources, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(sources) != len(job.InputSourceIds) {
		return nil, ErrStaleLease
	}
	for _, src := range sources {
		if src.State != kaguyamemorysource.StateClaimed || src.JobID != job.ID {
			return nil, ErrStaleLease
		}
	}
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled || policy.Epoch != row.PolicyEpoch {
		return func(ctx context.Context) error {
			return releaseClaimTx(ctx, client, job, "policy_changed", "memory policy changed")
		}, nil
	}
	// 重新检查会话模式与项目有效性，不能简单替换 epoch 后复活已撤销来源。
	if job.ConversationID != "" {
		convPolicy, err := ResolveConversationPolicy(ctx, client, job.ConversationID, policy)
		if err != nil || !convPolicy.Recall || convPolicy.Mode == ModeReadOnly {
			return func(ctx context.Context) error {
				return releaseClaimTx(ctx, client, job, "policy_changed", "conversation no longer allows memory")
			}, nil
		}
	}
	return nil, nil
}

// releaseClaimTx 保守放弃在途结果：来源回到待处理，作业取消并记录原因。
func releaseClaimTx(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob, code, summary string) error {
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...), kaguyamemorysource.StateEQ(kaguyamemorysource.StateClaimed)).
		SetState(kaguyamemorysource.StatePending).SetJobID("").Exec(ctx); err != nil {
		return err
	}
	return finishJobTx(ctx, client, job, PublishResult{}, code, summary)
}

// saveReviewProposalTx 把有界 PatchPlan 保存到 Job.result_json 等待用户处理；
// 来源仍关联该作业，不重新进入自动提炼循环。
func saveReviewProposalTx(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob, review []dtomemory.PagePatch) error {
	payload, err := json.Marshal(dtomemory.PatchPlan{SchemaVersion: dtomemory.ContractSchemaVersion, Changes: review})
	if err != nil {
		return err
	}
	if len(payload) > maxResultBytes {
		return fmt.Errorf("%w: review proposal exceeds size limit", ErrPlanInvalid)
	}
	return client.KaguyaMemoryJob.UpdateOneID(job.ID).SetResultJSON(string(payload)).Exec(ctx)
}

// completeSourcesTx 按覆盖范围推进来源游标：全部覆盖标 processed（无有效变更时
// noop），未覆盖分段回到待处理，不能把部分覆盖的来源标成已处理。
func completeSourcesTx(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob,
	projections []*SourceProjection, result PublishResult) error {
	// 无任何长期价值落库时标记 noop；其余消费过的来源标记 processed。
	state := kaguyamemorysource.StateProcessed
	if result.Published == 0 && result.Review == 0 {
		state = kaguyamemorysource.StateNoop
	}
	for _, projection := range projections {
		src, err := client.KaguyaMemorySource.Get(ctx, projection.SourceID)
		if err != nil {
			return err
		}
		cursor := src.CursorPart + len(projection.Segments)
		update := client.KaguyaMemorySource.UpdateOneID(src.ID).SetCursorPart(cursor)
		if cursor < src.CursorPart+projection.PartsTotal {
			update.SetState(kaguyamemorysource.StatePending).SetJobID("")
		} else {
			update.SetState(state).SetJobID(job.ID)
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// finishJobTx 写入 Job 终态；Job 终态与页面改动同事务。
func finishJobTx(ctx context.Context, client *ent.Client, job *ent.KaguyaMemoryJob, result PublishResult, code string, summary ...string) error {
	status := kaguyamemoryjob.StatusSucceeded
	if result.NeedsJob {
		status = kaguyamemoryjob.StatusNeedsReview
	}
	if code == "policy_changed" || code == "source_excluded" {
		status = kaguyamemoryjob.StatusCanceled
	}
	if code == "review_rejected" {
		status = kaguyamemoryjob.StatusCanceled
	}
	if code == "invalid_output" {
		status = kaguyamemoryjob.StatusFailed
	}
	if code == "retry_wait" {
		status = kaguyamemoryjob.StatusRetryWait
	}
	if code == "blocked" {
		status = kaguyamemoryjob.StatusBlocked
	}
	update := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		SetStatus(status).SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code)
	if len(summary) > 0 {
		update.SetErrorSummary(truncateRunes(summary[0], 500))
	}
	switch status {
	case kaguyamemoryjob.StatusSucceeded, kaguyamemoryjob.StatusNeedsReview,
		kaguyamemoryjob.StatusCanceled, kaguyamemoryjob.StatusFailed:
		update.SetFinishedAt(nowTime())
	}
	if status == kaguyamemoryjob.StatusSucceeded && resultJSONEmpty(job) {
		summaryJSON, err := json.Marshal(map[string]int{
			"published": result.Published, "noop": result.Noop, "dropped": result.Dropped,
		})
		if err != nil {
			return err
		}
		update.SetResultJSON(string(summaryJSON))
	}
	return update.Exec(ctx)
}

// resultJSONEmpty 判断 Job 是否还没有任何结果内容。
func resultJSONEmpty(job *ent.KaguyaMemoryJob) bool {
	return job.ResultJSON == ""
}

// candidatePagesTx 加载 PatchPlan 引用的候选页面集合（阶段 B 的小量完整旧页面）。
func candidatePagesTx(ctx context.Context, client *ent.Client, scope string, plan *dtomemory.PatchPlan) (map[string]*ent.KaguyaMemoryPage, error) {
	ids := map[string]bool{}
	for i := range plan.Changes {
		change := &plan.Changes[i]
		if change.PageID != "" {
			ids[change.PageID] = true
		}
		for _, related := range change.RelatedIDs {
			ids[related] = true
		}
	}
	out := make(map[string]*ent.KaguyaMemoryPage, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	pages, err := client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.IDIn(list...), kaguyamemorypage.ScopeKeyEQ(scope)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, page := range pages {
		out[page.ID] = page
	}
	return out, nil
}

// ApproveReview 以当前页面版本发布用户批准的待审提案；拒绝时记录处理结果。
func (s *Service) ApproveReview(ctx context.Context, job *ent.KaguyaMemoryJob) (PublishResult, error) {
	plan, err := reviewPlan(job)
	if err != nil {
		return PublishResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	row, err := client.KaguyaMemoryJob.Get(ctx, job.ID)
	if err != nil {
		return PublishResult{}, err
	}
	if row.Status != kaguyamemoryjob.StatusNeedsReview {
		return PublishResult{}, ErrStaleLease
	}
	if err := ValidateScope(ctx, client, row.ScopeKey); err != nil {
		return PublishResult{}, err
	}
	candidates, err := candidatePagesTx(ctx, client, job.ScopeKey, plan)
	if err != nil {
		return PublishResult{}, err
	}
	index := NewEvidenceIndex(nil)
	if err := LoadRetainedEvidence(ctx, client, index, candidates); err != nil {
		return PublishResult{}, err
	}
	// Approval is explicit, but it cannot revive deleted sources or replace
	// unavailable evidence with an unsupported assertion.
	for _, change := range plan.Changes {
		for _, claim := range change.Claims {
			for _, ev := range claim.Evidence {
				src, err := client.KaguyaMemorySource.Get(ctx, ev.SourceID)
				if ent.IsNotFound(err) || (err == nil && src.State == kaguyamemorysource.StateExcluded) {
					return PublishResult{}, fmt.Errorf("%w: proposal source is no longer available", ErrPlanInvalid)
				}
				if err != nil {
					return PublishResult{}, err
				}
				if !index.allows(change.PageID, ev) {
					if src.ScopeKey != row.ScopeKey {
						return PublishResult{}, ErrPageForbidden
					}
					if err := validateSourceQuote(ctx, client, src, ev.PartKey, ev.Quote); err != nil {
						return PublishResult{}, err
					}
				}
			}
		}
	}
	result := PublishResult{}
	for i := range plan.Changes {
		change := &plan.Changes[i]
		// 批准时以当前页面版本发布新修订，不回退版本号。
		if page, ok := candidates[change.PageID]; ok && change.Action != "create" {
			change.BaseVersion = page.Version
		}
		outcome, err := applyChangeTx(ctx, client, job, change, candidates, applyOptions{actor: actorUser, approved: true})
		if err != nil {
			return PublishResult{}, err
		}
		switch outcome {
		case changePublished:
			result.Published++
		case changeNoop:
			result.Noop++
		case changeDropped:
			result.Dropped++
		case changeReview:
			return PublishResult{}, ErrPageVersionConflict
		}
	}
	if err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		SetStatus(kaguyamemoryjob.StatusSucceeded).SetResultJSON("").SetFinishedAt(nowTime()).Exec(ctx); err != nil {
		return PublishResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

// RejectReview 记录用户拒绝待审提案的处理结果，不产生新修订。
func (s *Service) RejectReview(ctx context.Context, job *ent.KaguyaMemoryJob) error {
	return s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusNeedsReview)).
		SetStatus(kaguyamemoryjob.StatusCanceled).SetErrorCode("review_rejected").
		SetErrorSummary("proposal rejected by user").SetFinishedAt(nowTime()).Exec(ctx)
}

// reviewPlan 解析 Job 中保存的有界待审 PatchPlan。
func reviewPlan(job *ent.KaguyaMemoryJob) (*dtomemory.PatchPlan, error) {
	var plan dtomemory.PatchPlan
	if err := json.Unmarshal([]byte(job.ResultJSON), &plan); err != nil {
		return nil, fmt.Errorf("decode review proposal: %w", err)
	}
	return &plan, nil
}

// LintStats 是确定性完整性检查的结果计数，不包含原文。
type LintStats struct {
	OrphanLinks      int // 孤立或指向已删除页面的关系
	ExcludedEvidence int // 引用了已排除来源的证据残留
	ExpiredPages     int // 已过期但仍在有效状态的页面（仅提示，不自动删除）
	DuplicateTitles  int // 同范围同标题的可疑重复（仅提示；canonical_key 唯一由数据库保证）
}

// LintPass 执行周期性确定性检查（docs/memory-design.md 7.6）：
// 孤立引用、已撤销来源、过期页面、重复 canonical key。不做语义合并——
// 语义合并/矛盾检查由用户手动触发并产生待审提案。孤立关系直接清理，
// 其余只统计并记录安全计数，不写入原文。
func (s *Service) LintPass(ctx context.Context) (LintStats, error) {
	var stats LintStats
	pages, err := s.client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.StatusEQ(kaguyamemorypage.StatusDeleted)).
		Select(kaguyamemorypage.FieldID).All(ctx)
	if err != nil {
		return stats, err
	}
	dead := map[string]bool{}
	for _, page := range pages {
		dead[page.ID] = true
	}
	links, err := s.client.KaguyaMemoryLink.Query().All(ctx)
	if err != nil {
		return stats, err
	}
	for _, link := range links {
		if dead[link.FromPageID] || dead[link.ToPageID] {
			stats.OrphanLinks++
			if _, err := s.client.KaguyaMemoryLink.Delete().Where(kaguyamemorylink.IDEQ(link.ID)).Exec(ctx); err != nil {
				return stats, err
			}
		}
	}
	evidence, err := s.client.KaguyaMemoryEvidence.Query().
		Select(kaguyamemoryevidence.FieldID, kaguyamemoryevidence.FieldSourceID).All(ctx)
	if err != nil {
		return stats, err
	}
	sources, err := s.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.StateEQ(kaguyamemorysource.StateExcluded)).
		Select(kaguyamemorysource.FieldID, kaguyamemorysource.FieldSourceKey).All(ctx)
	if err != nil {
		return stats, err
	}
	excluded := map[string]bool{}
	for _, src := range sources {
		excluded[src.ID] = true
	}
	for _, row := range evidence {
		if excluded[row.SourceID] {
			stats.ExcludedEvidence++
		}
	}
	active, err := s.client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.StatusEQ(kaguyamemorypage.StatusActive), kaguyamemorypage.DeletedAtIsNil()).
		All(ctx)
	if err != nil {
		return stats, err
	}
	now := nowTime()
	titles := map[string]int{}
	for _, page := range active {
		if page.ExpiresAt != nil && page.ExpiresAt.Before(now) {
			stats.ExpiredPages++
		}
		titles[page.ScopeKey+"\x00"+strings.ToLower(strings.TrimSpace(page.Title))]++
	}
	for _, count := range titles {
		if count > 1 {
			stats.DuplicateTitles++
		}
	}
	return stats, nil
}
