package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// maxImportBytes 复用项目资料读取的既有大小上限；超限直接拒绝，
// 不截断后假装导入成功。
const maxImportBytes = 512 << 10

// Document 是复用项目路径校验、忽略规则与大小限制读取的导入资料快照。
type Document struct {
	Path    string
	Content string
	Size    int64
}

// DocumentReader 由宿主注入（项目服务实现），复用现有路径安全能力；
// memory 包不反向依赖项目服务，避免循环依赖。
type DocumentReader interface {
	ReadDocument(ctx context.Context, projectID, path string, maxBytes int64) (*Document, error)
}

// WithDocumentReader 注入导入资料读取器；未注入时导入接口明确报错。
func (s *Service) WithDocumentReader(reader DocumentReader) *Service {
	s.documents = reader
	return s
}

// ImportDocument 显式导入一份项目资料：读取有界快照 → 规范化投影 → 写入
// import 来源 → 交给后台 Worker 编译。同路径同内容重复导入幂等返回已有来源；
// 内容变化产生新来源，历史证据不受影响。
func (s *Service) ImportDocument(ctx context.Context, req *dtomemory.MemoryImportReq) (*dtomemory.MemoryImportResp, error) {
	if s.documents == nil {
		return nil, fmt.Errorf("%w: document reader is not configured", ErrPlanInvalid)
	}
	if !strings.HasPrefix(req.ScopeKey, scopeProjectPrefix) {
		return nil, fmt.Errorf("%w: imports require a project scope", ErrPlanInvalid)
	}
	if err := ValidateScope(ctx, s.client, req.ScopeKey); err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, fmt.Errorf("%w: memory is disabled", ErrPlanInvalid)
	}
	projectID := strings.TrimPrefix(req.ScopeKey, scopeProjectPrefix)
	doc, err := s.documents.ReadDocument(ctx, projectID, req.Path, maxImportBytes)
	if err != nil {
		return nil, err
	}
	segments := BuildDocumentSegments(doc.Content)
	if len(segments) == 0 {
		return nil, fmt.Errorf("%w: imported document has no visible text", ErrPlanInvalid)
	}
	hash := ProjectionHash(segments)
	key := importSourceKey(projectID, doc.Path, hash)
	existing, err := s.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx)
	if err == nil {
		return importResp(existing, doc, true), nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	row, err := s.client.KaguyaMemorySource.Create().
		SetSourceKey(key).
		SetKind(kaguyamemorysource.KindImport).
		SetScopeKey(req.ScopeKey).
		SetProjectionVersion(ProjectionVersion).
		SetContentHash(hash).
		SetRawContent(doc.Content).
		SetDocumentPath(doc.Path).
		SetState(kaguyamemorysource.StatePending).
		SetCapturedAt(nowTime()).
		SetPolicyEpoch(policy.Epoch).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发重复导入：唯一约束去重，沿用已有来源。
		existing, queryErr := s.client.KaguyaMemorySource.Query().
			Where(kaguyamemorysource.SourceKeyEQ(key)).Only(ctx)
		if queryErr != nil {
			return nil, queryErr
		}
		return importResp(existing, doc, true), nil
	}
	if err != nil {
		return nil, err
	}
	Notify()
	return importResp(row, doc, false), nil
}

func importResp(src *ent.KaguyaMemorySource, doc *Document, deduplicated bool) *dtomemory.MemoryImportResp {
	return &dtomemory.MemoryImportResp{
		SourceID: src.ID, State: string(src.State), Path: doc.Path,
		Size: doc.Size, Deduplicated: deduplicated,
	}
}

// importSourceKey 以项目、路径与内容哈希构成稳定键：同内容幂等，内容变化是新来源。
func importSourceKey(projectID, path, contentHash string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + path + "\x00" + contentHash))
	return "import:" + hex.EncodeToString(sum[:16])
}
