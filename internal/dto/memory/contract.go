package memory

// 本文件定义后台编译的版本化 JSON 契约与修订 claims 的持久化结构，
// 供 internal/ent/schema 与 internal/service/memory 共同引用；不含 HTTP DTO。

// ContractSchemaVersion 是提炼与 PatchPlan 响应的 JSON 契约版本。
const ContractSchemaVersion = 1

// CandidateEvidence 是阶段 A 提炼候选引用的来源片段。
type CandidateEvidence struct {
	SourceID string `json:"source_id"`
	PartKey  string `json:"part_key"`
	Quote    string `json:"quote"`
}

// Candidate 是阶段 A 输出的待整合主张，不直接落库。
type Candidate struct {
	Key       string             `json:"key"`
	Kind      string             `json:"kind"`
	Title     string             `json:"title"`
	Statement string             `json:"statement"`
	Aliases   []string           `json:"aliases"`
	Basis     string             `json:"basis"`
	Evidence  []CandidateEvidence `json:"evidence"`
}

// ExtractResult 是阶段 A 的完整响应。
type ExtractResult struct {
	SchemaVersion int         `json:"schema_version"`
	Candidates    []Candidate `json:"candidates"`
}

// ClaimEvidence 是 PatchPlan 中主张引用的证据；relation 缺省为 support。
type ClaimEvidence struct {
	SourceID string `json:"source_id"`
	PartKey  string `json:"part_key"`
	Quote    string `json:"quote"`
	Relation string `json:"relation,omitempty"`
}

// ClaimPatch 是 PatchPlan 中单条主张及其证据。
type ClaimPatch struct {
	Key       string         `json:"key"`
	Statement string         `json:"statement"`
	Basis     string         `json:"basis"`
	Evidence  []ClaimEvidence `json:"evidence"`
}

// PagePatch 是一次页面变更提案；update/conflict 使用完整页面替换 + base_version。
// 契约不包含 scope_key、pinned、user_locked 等发布权限字段。
type PagePatch struct {
	Action        string       `json:"action"` // create/update/noop/conflict
	CandidateKeys []string     `json:"candidate_keys"`
	PageID        string       `json:"page_id,omitempty"`
	BaseVersion   int64        `json:"base_version,omitempty"`
	CanonicalKey  string       `json:"canonical_key,omitempty"`
	Kind          string       `json:"kind"`
	Title         string       `json:"title"`
	Summary       string       `json:"summary"`
	Body          string       `json:"body"`
	Aliases       []string     `json:"aliases"`
	Claims        []ClaimPatch `json:"claims"`
	RelatedIDs    []string     `json:"related_ids"`
	Reason        string       `json:"reason"`
}

// PatchPlan 是阶段 C 的结构化变更计划。
type PatchPlan struct {
	SchemaVersion int         `json:"schema_version"`
	Changes       []PagePatch `json:"changes"`
}

// MemoryClaim 是已发布修订中保存的主张摘要；完整证据挂在 KaguyaMemoryEvidence。
type MemoryClaim struct {
	Key       string `json:"key"`
	Statement string `json:"statement"`
	Basis     string `json:"basis"`
}

// TurnMemoryRef 记录一轮自动召回选中的页面版本；不复制正文，
// 页面被删除后引用只能显示“已删除”，不能通过历史接口绕过删除。
type TurnMemoryRef struct {
	PageID  string `json:"page_id"`
	Version int64  `json:"version"`
}

// TurnMemorySelection 是轮次元数据中的召回记录。
type TurnMemorySelection struct {
	RetrieverVersion int             `json:"retriever_version"`
	EstimatedTokens  int64           `json:"estimated_tokens"`
	Refs             []TurnMemoryRef `json:"refs"`
}
