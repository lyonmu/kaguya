package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
)

// CompareRevisions 对比任意两个已发布修订（from=0 表示 to 的前一个版本）；
// 结果稳定、可解释：字段、主张与证据都按固定顺序排列，不依赖不确定迭代。
func (s *Service) CompareRevisions(ctx context.Context, scopes []string, pageID string, from, to int64) (*dtomemory.MemoryDiffResp, error) {
	if err := validateScopes(scopes); err != nil {
		return nil, err
	}
	page, err := s.client.KaguyaMemoryPage.Query().Where(kaguyamemorypage.IDEQ(pageID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	if page.DeletedAt != nil || page.Status == kaguyamemorypage.StatusDeleted {
		return nil, ErrPageNotFound
	}
	allowed := false
	for _, scope := range scopes {
		if scope == page.ScopeKey {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, ErrPageForbidden
	}
	if to == 0 {
		to = page.Version
	}
	if from == 0 {
		previous, err := s.client.KaguyaMemoryRevision.Query().
			Where(kaguyamemoryrevision.PageIDEQ(pageID), kaguyamemoryrevision.VersionLT(to)).
			Order(ent.Desc(kaguyamemoryrevision.FieldVersion)).First(ctx)
		if ent.IsNotFound(err) {
			return nil, ErrPageVersionGone
		}
		if err != nil {
			return nil, err
		}
		from = previous.Version
	}
	fromRev, err := s.revisionForDiff(ctx, pageID, from)
	if err != nil {
		return nil, err
	}
	toRev, err := s.revisionForDiff(ctx, pageID, to)
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemoryDiffResp{
		PageID:          pageID,
		From:            diffSide(fromRev.revision),
		To:              diffSide(toRev.revision),
		ContentChanges:  []dtomemory.MemoryFieldChangeResp{},
		MetadataChanges: []dtomemory.MemoryFieldChangeResp{},
		ClaimChanges:    []dtomemory.MemoryClaimChangeResp{},
		EvidenceChanges: []dtomemory.MemoryEvidenceChangeResp{},
	}
	addField := func(dst *[]dtomemory.MemoryFieldChangeResp, field, from, to string) {
		if from != to {
			*dst = append(*dst, dtomemory.MemoryFieldChangeResp{Field: field, From: from, To: to})
		}
	}
	addField(&resp.ContentChanges, "title", fromRev.revision.Title, toRev.revision.Title)
	addField(&resp.ContentChanges, "summary", fromRev.revision.Summary, toRev.revision.Summary)
	addField(&resp.ContentChanges, "body", fromRev.revision.Body, toRev.revision.Body)
	addField(&resp.MetadataChanges, "kind", string(fromRev.revision.Kind), string(toRev.revision.Kind))
	addField(&resp.MetadataChanges, "status", string(fromRev.revision.Status), string(toRev.revision.Status))
	addField(&resp.MetadataChanges, "canonical_key", fromRev.revision.CanonicalKey, toRev.revision.CanonicalKey)
	addField(&resp.MetadataChanges, "aliases", strings.Join(fromRev.revision.Aliases, "、"), strings.Join(toRev.revision.Aliases, "、"))
	addField(&resp.MetadataChanges, "pinned", boolLabel(fromRev.revision.Pinned), boolLabel(toRev.revision.Pinned))
	addField(&resp.MetadataChanges, "user_locked", boolLabel(fromRev.revision.UserLocked), boolLabel(toRev.revision.UserLocked))
	addField(&resp.MetadataChanges, "expires_at", timeLabel(fromRev.revision.ExpiresAt), timeLabel(toRev.revision.ExpiresAt))
	resp.ClaimChanges = diffClaims(fromRev.revision.Claims, toRev.revision.Claims)
	resp.EvidenceChanges = diffEvidence(fromRev.evidence, toRev.evidence)
	return resp, nil
}

// diffRevision 是一个修订及其证据行的冻结快照。
type diffRevision struct {
	revision *ent.KaguyaMemoryRevision
	evidence []*ent.KaguyaMemoryEvidence
}

func (s *Service) revisionForDiff(ctx context.Context, pageID string, version int64) (*diffRevision, error) {
	revision, err := s.client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(pageID), kaguyamemoryrevision.VersionEQ(version)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageVersionGone
	}
	if err != nil {
		return nil, err
	}
	rows, err := revisionEvidenceRows(ctx, s.client, revision.ID)
	if err != nil {
		return nil, err
	}
	return &diffRevision{revision: revision, evidence: rows}, nil
}

func diffSide(revision *ent.KaguyaMemoryRevision) dtomemory.MemoryDiffSideResp {
	return dtomemory.MemoryDiffSideResp{
		Version: revision.Version, Actor: string(revision.Actor), Reason: revision.Reason,
		CreatedAt: revision.CreatedAt, Title: revision.Title, Summary: revision.Summary,
		Body: revision.Body, Kind: string(revision.Kind), Status: string(revision.Status),
		Aliases: append([]string{}, revision.Aliases...),
	}
}

// diffClaims 对比主张：added / removed / changed，按 claim key 排序。
func diffClaims(from, to []dtomemory.MemoryClaim) []dtomemory.MemoryClaimChangeResp {
	fromByKey := make(map[string]dtomemory.MemoryClaim, len(from))
	for _, claim := range from {
		fromByKey[claim.Key] = claim
	}
	toByKey := make(map[string]dtomemory.MemoryClaim, len(to))
	for _, claim := range to {
		toByKey[claim.Key] = claim
	}
	keys := make([]string, 0, len(fromByKey)+len(toByKey))
	seen := map[string]bool{}
	for _, claim := range append(append([]dtomemory.MemoryClaim{}, from...), to...) {
		if !seen[claim.Key] {
			seen[claim.Key] = true
			keys = append(keys, claim.Key)
		}
	}
	sort.Strings(keys)
	out := make([]dtomemory.MemoryClaimChangeResp, 0, len(keys))
	for _, key := range keys {
		before, hasBefore := fromByKey[key]
		after, hasAfter := toByKey[key]
		switch {
		case !hasBefore:
			out = append(out, dtomemory.MemoryClaimChangeResp{Key: key, Change: "added", To: after.Statement, ToBasis: after.Basis})
		case !hasAfter:
			out = append(out, dtomemory.MemoryClaimChangeResp{Key: key, Change: "removed", From: before.Statement, FromBasis: before.Basis})
		case before.Statement != after.Statement || before.Basis != after.Basis:
			out = append(out, dtomemory.MemoryClaimChangeResp{
				Key: key, Change: "changed", From: before.Statement, To: after.Statement,
				FromBasis: before.Basis, ToBasis: after.Basis,
			})
		}
	}
	return out
}

type evidenceChangeKey struct {
	claimKey string
	sourceID string
	partKey  string
	quote    string
}

// diffEvidence 对比证据：added / removed / changed（关系或依据变化），
// 按 claim key、来源、片段与摘录排序。
func diffEvidence(from, to []*ent.KaguyaMemoryEvidence) []dtomemory.MemoryEvidenceChangeResp {
	index := func(rows []*ent.KaguyaMemoryEvidence) map[evidenceChangeKey]*ent.KaguyaMemoryEvidence {
		out := make(map[evidenceChangeKey]*ent.KaguyaMemoryEvidence, len(rows))
		for _, row := range rows {
			out[evidenceChangeKey{row.ClaimKey, row.SourceID, row.PartKey, row.Quote}] = row
		}
		return out
	}
	fromByKey, toByKey := index(from), index(to)
	keys := make([]evidenceChangeKey, 0, len(fromByKey)+len(toByKey))
	seen := map[evidenceChangeKey]bool{}
	for _, key := range append(evidenceKeys(from), evidenceKeys(to)...) {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].claimKey != keys[j].claimKey {
			return keys[i].claimKey < keys[j].claimKey
		}
		if keys[i].sourceID != keys[j].sourceID {
			return keys[i].sourceID < keys[j].sourceID
		}
		if keys[i].partKey != keys[j].partKey {
			return keys[i].partKey < keys[j].partKey
		}
		return keys[i].quote < keys[j].quote
	})
	out := make([]dtomemory.MemoryEvidenceChangeResp, 0, len(keys))
	for _, key := range keys {
		before, hasBefore := fromByKey[key]
		after, hasAfter := toByKey[key]
		item := dtomemory.MemoryEvidenceChangeResp{
			ClaimKey: key.claimKey, SourceID: key.sourceID, PartKey: key.partKey,
		}
		switch {
		case !hasBefore:
			item.Change, item.Quote, item.Relation, item.Basis = "added", after.Quote, string(after.Relation), string(after.Basis)
		case !hasAfter:
			item.Change, item.Quote, item.Relation, item.Basis = "removed", before.Quote, string(before.Relation), string(before.Basis)
		case before.Relation != after.Relation || before.Basis != after.Basis:
			item.Change, item.Quote = "changed", after.Quote
			item.Relation, item.Basis = string(after.Relation), string(after.Basis)
		default:
			continue
		}
		out = append(out, item)
	}
	return out
}

func evidenceKeys(rows []*ent.KaguyaMemoryEvidence) []evidenceChangeKey {
	keys := make([]evidenceChangeKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, evidenceChangeKey{row.ClaimKey, row.SourceID, row.PartKey, row.Quote})
	}
	return keys
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func timeLabel(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
