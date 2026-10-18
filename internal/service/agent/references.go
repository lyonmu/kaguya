package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/workspace"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

// 引用解析的硬边界：扫描时间、manifest 元信息、单文件行数与字节窗口。
// 这些是资源边界，不是“最多引用 N 个文件”的数量上限。
const (
	referenceMaxPathLen    = 4096
	referenceScanBudget    = 2 * time.Second
	referenceMetadataBytes = 64 << 10
	referenceUnknownBytes  = 256 << 10
	referenceLineLimit     = 2000
	referenceLineByteLimit = 50 * 1024
	referenceMaxFileBytes  = 32 * 1024 * 1024
)

var (
	// ErrReferenceInvalid 表示引用路径非法、跨工作区或类型与真实条目不一致。
	ErrReferenceInvalid = errors.New("reference path is invalid")
	// ErrReferenceReadFailure 表示显式引用的文件读取失败。
	ErrReferenceReadFailure = errors.New("reference content read failed")
	// ErrContextBudgetExceeded 表示预算不足以装入任何引用内容，不发送仅路径的伪上下文。
	ErrContextBudgetExceeded = errors.New("context budget is insufficient for references")
)

// referenceEntry 是一个实际读取或跳过的候选文件。
type referenceEntry struct {
	path                string
	resolvedPath        string
	info                os.FileInfo
	canonical           *referenceEntry
	fragmentHash        string
	manifestReservation int64
	manifestOverhead    int64
	headerReservation   int64
	loaded              bool
	text                string
	totalLines          int
	nextOffset          int
	includedLines       int
	skipReason          string
	duplicate           bool
	included            bool
}

// referenceSource 是一个显式文件或目录来源的解析结果。
type referenceSource struct {
	ref     dtochat.Reference
	entries []*referenceEntry
	// explicit 表示来源是显式文件；其读取失败必须终止解析。
	explicit                bool
	warnings                []string
	partialReason           string
	lastPath                string
	omitted                 int
	manifestFiles           int
	continuationReservation int64
	scanIncomplete          bool
}

// referenceResolution 是交给 prompt 格式化器的结果；summary 写入轮次元信息。
type referenceResolution struct {
	prompt  string
	summary *dtochat.TurnReferenceSummary
}

// normalizeReferences 把旧 files 映射为 file 引用，按 type/path 去重并保持用户顺序。
func normalizeReferences(files []string, refs []dtochat.Reference) ([]dtochat.Reference, error) {
	out := make([]dtochat.Reference, 0, len(files)+len(refs))
	seen := map[string]bool{}
	all := make([]dtochat.Reference, 0, len(files)+len(refs))
	for _, file := range files {
		all = append(all, dtochat.Reference{Type: "file", Path: file})
	}
	all = append(all, refs...)
	for _, ref := range all {
		if ref.Type != "file" && ref.Type != "directory" || len(ref.Name) > 256 {
			return nil, ErrReferenceInvalid
		}
		clean, err := workspace.CleanRelative(ref.Path, referenceMaxPathLen)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid relative reference path", ErrReferenceInvalid)
		}
		ref.Path = path.Clean(filepath.ToSlash(clean))
		key := ref.Type + "\x00" + ref.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out, nil
}

// referenceByteBudget 使用压缩后的实际 messages，system/user 已在其中，不能重复计费。
// 已知窗口按每新增 UTF-8 byte 预留一个 estimated token；未知窗口沿用 256KiB。
func referenceByteBudget(compactor *contextCompactor, messages []fantasy.Message) int64 {
	if compactor.window <= 0 {
		return referenceUnknownBytes
	}
	budget := computeContextBudget(contextBudgetInput{
		window: int64(compactor.window), compactionPercent: compactor.percent,
		historyTokens: estimateMessages(messages), toolSchemaTokens: compactor.toolTokens,
		memoryTokens: compactor.transientTokens, outputReserve: compactor.outputReserve,
	})
	return budget.referenceBudget
}

// resolveReferenceContext 解析全部来源：显式文件优先，目录递归按路径字典序补充；
// 同一实际身份的正文只读取一次。目录内容始终标注为数据，不覆盖指令。
func resolveReferenceContext(ctx context.Context, projectID string, refs []dtochat.Reference, budgetBytes int64) (*referenceResolution, error) {
	return resolveReferenceContextWithBudget(ctx, projectID, refs, budgetBytes, false)
}

func resolveReferenceContextWithBudget(ctx context.Context, projectID string, refs []dtochat.Reference, budgetBytes int64, known bool) (*referenceResolution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if budgetBytes < 0 {
		return nil, ErrContextBudgetExceeded
	}

	projectPath, err := (&projectsvc.ProjectSvc{}).Workspace(ctx, projectID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: project workspace unavailable", ErrReferenceInvalid)
	}
	root, err := workspace.Open(projectPath)
	if err != nil {
		return nil, fmt.Errorf("%w: open workspace", ErrReferenceInvalid)
	}
	defer root.Close()
	// ignore、元信息与正文共享扫描预算；父 ctx 取消仍优先返回取消错误。
	scanCtx, cancelScan := context.WithTimeout(ctx, referenceScanBudget)
	defer cancelScan()
	matcher, err := workspace.NewIgnoreMatcher(scanCtx, root)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: read ignore rules", ErrReferenceReadFailure)
	}

	reader := &referenceReader{filter: workspace.NewReferenceFilter(scanCtx, root, matcher), ctx: ctx, scanCtx: scanCtx, budget: budgetBytes, budgetKnown: known, metadata: workspace.ReferenceMetadataBudget{Limit: referenceMetadataBytes}}
	for _, ref := range explicitFilesFirst(refs) {
		if err := reader.addSource(ref); err != nil {
			return nil, err
		}
	}
	return reader.format()
}

// explicitFilesFirst 稳定地把显式文件排在目录之前：用户顺序内，显式文件优先装入
// 内容，重叠的目录条目只保留占用标记。
func explicitFilesFirst(refs []dtochat.Reference) []dtochat.Reference {
	ordered := make([]dtochat.Reference, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == "file" {
			ordered = append(ordered, ref)
		}
	}
	for _, ref := range refs {
		if ref.Type != "file" {
			ordered = append(ordered, ref)
		}
	}
	return ordered
}

type referenceReader struct {
	filter        *workspace.ReferenceFilter
	ctx           context.Context
	scanCtx       context.Context
	budget        int64
	bodyAllowance int64
	budgetKnown   bool
	writer        *referenceBudgetWriter
	metadata      workspace.ReferenceMetadataBudget
	// readWindow 是同句柄读取的窄测试 seam；生产使用 workspace 原语。
	readWindow func(context.Context, *os.File) (workspace.TextWindow, error)

	sources       []*referenceSource
	cache         []*referenceEntry // 同轮实际身份；只保留已读取窗口，不跨轮复用
	scanTruncated bool
	includedFiles int
	// deferredContent 记录“有可读内容但因预算未装入”：只有这种情况才报预算不足。
	deferredContent bool
}

// addSource 解析一个来源；显式文件失败终止，目录条目失败只记录 warning。
func (r *referenceReader) addSource(ref dtochat.Reference) error {
	clean, err := workspace.CleanRelative(ref.Path, referenceMaxPathLen)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrReferenceInvalid, err)
	}
	slash := path.Clean(strings.ReplaceAll(clean, string(os.PathSeparator), "/"))
	source := &referenceSource{ref: dtochat.Reference{Type: ref.Type, Path: slash}, lastPath: slash}
	source.explicit = ref.Type == "file"
	// 预校验来源也必须有界；format 会以完整输出结构重建此账本。
	encoded, err := json.Marshal(referenceSourcePrototype(source))
	if err != nil || !r.metadata.Take(int64(len(encoded)+1)) {
		return ErrContextBudgetExceeded
	}
	r.sources = append(r.sources, source)
	switch ref.Type {
	case "file":
		source.explicit = true
		entry := &referenceEntry{path: slash}
		proof, err := r.filter.Check(slash, "file", false)
		if err != nil {
			return r.referenceAccessError(err)
		}
		entry.resolvedPath, entry.info = proof.Path, proof.Info
		source.entries = append(source.entries, entry)
	case "directory":
		if err := r.validateExplicitDirectory(slash); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown reference type %q", ErrReferenceInvalid, ref.Type)
	}
	return nil
}

// validateExplicitDirectory 要求显式目录是真实目录；符号链接目录别名拒绝。
func (r *referenceReader) validateExplicitDirectory(slash string) error {
	if _, err := r.filter.Check(slash, "directory", false); err != nil {
		if errors.Is(err, workspace.ErrReferenceType) || errors.Is(err, workspace.ErrReferenceUnavailable) {
			return ErrReferenceInvalid
		}
		return r.referenceAccessError(err)
	}
	return nil
}

// referenceAccessError 保持有限安全错误，不向 API/模型回显 OS 路径或 ignore 正文。
func (r *referenceReader) referenceAccessError(err error) error {
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if errors.Is(err, workspace.ErrReferenceFiltered) || errors.Is(err, workspace.ErrReferenceType) {
		return ErrReferenceInvalid
	}
	if errors.Is(err, workspace.ErrReferenceIgnore) {
		return fmt.Errorf("%w: %w", ErrReferenceReadFailure, workspace.ErrReferenceIgnore)
	}
	return ErrReferenceReadFailure
}

// walkDirectory 递归收集目录内文本候选；不跟随任何符号链接，忽略规则严格生效。
func (r *referenceReader) walkDirectory(dir string, source *referenceSource) error {
	scan, err := r.filter.WalkReferences(dir, &r.metadata, func(kind, name string, info os.FileInfo) error {
		if kind != "file" {
			return nil
		}
		entry := &referenceEntry{path: name, resolvedPath: name, info: info}
		if err := r.admitReferenceEntry(source, entry); err != nil {
			source.omitted++
			return err
		}
		source.entries = append(source.entries, entry)
		return nil
	})
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrContextBudgetExceeded):
			source.partialReason = referencePartialReason(source.partialReason, "context_budget")
		case r.scanCtx.Err() != nil:
			source.partialReason = referencePartialReason(source.partialReason, "deadline")
		default:
			return r.referenceAccessError(err)
		}
		r.scanTruncated = true
		source.scanIncomplete = true
	}
	if scan.Incomplete {
		source.partialReason = referencePartialReason(source.partialReason, scan.Reason)
		r.scanTruncated = true
		source.scanIncomplete = true
	}
	// walker 的 warning 字符串已计费；转成 JSON 关联时替换，不能重复计费。
	for _, warning := range scan.Warnings {
		r.metadata.Used -= int64(len(warning))
	}
	for _, warning := range scan.Warnings {
		if err := r.admitReferenceWarning(source, warning); err != nil {
			reason := "metadata_budget"
			if errors.Is(err, ErrContextBudgetExceeded) {
				reason = "context_budget"
			}
			source.partialReason = referencePartialReason(source.partialReason, reason)
			r.scanTruncated = true
			source.scanIncomplete = true
			break
		}
	}
	// 字节预算保证候选集合有界；排序前后都检查合作式扫描期限。
	if r.scanCtx.Err() != nil {
		source.partialReason = referencePartialReason(source.partialReason, "deadline")
	}
	sort.Slice(source.entries, func(i, j int) bool { return source.entries[i].path < source.entries[j].path })
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if r.scanCtx.Err() != nil {
		source.partialReason = referencePartialReason(source.partialReason, "deadline")
	}
	return nil
}

// loadEntry 读取一个普通文本文件的窗口；二进制/超限只记录 safe 原因。
func (r *referenceReader) loadEntry(entry *referenceEntry) error {
	if entry.loaded {
		return nil
	}
	entry.loaded = true
	file, proof, err := r.filter.OpenFile(entry.path, false)
	if err != nil {
		return r.referenceAccessError(err)
	}
	defer file.Close()
	if entry.resolvedPath != "" && entry.resolvedPath != proof.Path {
		return ErrReferenceReadFailure
	}
	if entry.info != nil && (!os.SameFile(entry.info, proof.Info) || entry.info.Mode() != proof.Info.Mode() || entry.info.Size() != proof.Info.Size() || !entry.info.ModTime().Equal(proof.Info.ModTime())) {
		return ErrReferenceReadFailure
	}
	entry.resolvedPath, entry.info = proof.Path, proof.Info
	for _, cached := range r.cache {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if !os.SameFile(cached.info, proof.Info) {
			continue
		}
		if cached.info.Size() != proof.Info.Size() || !cached.info.ModTime().Equal(proof.Info.ModTime()) {
			return ErrReferenceReadFailure
		}
		if err := r.filter.Verify(proof, file); err != nil {
			return r.referenceAccessError(err)
		}
		entry.canonical = cached
		entry.duplicate, entry.included = cached.included, cached.included
		entry.skipReason, entry.totalLines, entry.includedLines, entry.nextOffset = cached.skipReason, cached.totalLines, cached.includedLines, cached.nextOffset
		return nil
	}
	readWindow := r.readWindow
	if readWindow == nil {
		readWindow = func(ctx context.Context, file *os.File) (workspace.TextWindow, error) {
			return workspace.ReadTextWindowFile(ctx, file, 0, referenceLineLimit, referenceMaxFileBytes, referenceLineByteLimit, min(referenceLineByteLimit, max(0, r.bodyAllowance)))
		}
	}
	window, err := readWindow(r.scanCtx, file)
	if err != nil {
		return r.referenceAccessError(err)
	}
	if err := r.filter.Verify(proof, file); err != nil {
		return r.referenceAccessError(err)
	}
	switch {
	case window.Binary:
		entry.skipReason = "binary or invalid UTF-8"
	case window.Grown:
		entry.skipReason = "file exceeds the 32MiB safety limit; use read with a line range"
	case window.OversizedLine:
		entry.skipReason = "first line exceeds the per-line output limit; use read with a narrower range"
	default:
		entry.text = string(window.Content)
		if proof.Info.Size() == 0 {
			entry.skipReason = "empty"
		}
		if !utf8.ValidString(entry.text) {
			entry.text = ""
			entry.skipReason = "binary or invalid UTF-8"
			return nil
		}
		if window.OutputLines == 0 && proof.Info.Size() > 0 {
			r.deferredContent = true
		}
		entry.totalLines = window.Lines
		lines := window.OutputLines
		entry.includedLines = lines
		if window.Lines > lines {
			entry.nextOffset = lines + 1
		}
	}
	r.cache = append(r.cache, entry)
	return nil
}

// sourceHash 记录实际装入内容的指纹；不保存第二份正文。
func (r *referenceReader) sourceHash(source *referenceSource) string {
	var hashInput strings.Builder
	for _, entry := range source.entries {
		if !entry.included {
			continue
		}
		hashInput.WriteString(entry.path)
		hashInput.WriteString("\x00")
		body := entry
		if entry.canonical != nil {
			body = entry.canonical
		}
		hashInput.WriteString(body.fragmentHash)
	}
	if hashInput.Len() == 0 {
		return ""
	}
	return textFingerprint(hashInput.String())
}

// sourceRanges 记录显式文件的包含行范围；目录来源以 manifest 为准。
func sourceRanges(source *referenceSource) []dtochat.TurnReferenceRange {
	if source.ref.Type != "file" {
		return nil
	}
	entry := source.entries[0]
	if !entry.included {
		return nil
	}
	return []dtochat.TurnReferenceRange{{Start: 1, End: entry.includedLines}}
}

func sourceFiles(source *referenceSource) ([]dtochat.TurnReferenceFile, bool) {
	files := make([]dtochat.TurnReferenceFile, 0, len(source.entries))
	included := false
	for _, entry := range source.entries {
		files = append(files, referenceFile(entry))
		included = included || entry.included
	}
	return files, included
}

func referenceFile(entry *referenceEntry) dtochat.TurnReferenceFile {
	file := dtochat.TurnReferenceFile{Path: entry.path, ResolvedPath: entry.resolvedPath, Included: entry.included, Status: "not_included"}
	switch entry.skipReason {
	case "empty":
		file.Status = "empty"
	case "binary or invalid UTF-8":
		file.Status = "binary"
	case "file exceeds the 32MiB safety limit; use read with a line range":
		file.Status = "too_large"
	case "first line exceeds the per-line output limit; use read with a narrower range":
		file.Status = "oversized_line"
	case "read failed":
		file.Status = "read_failed"
	}
	if entry.included {
		body := entry
		if entry.canonical != nil {
			body = entry.canonical
		}
		file.Status, file.Hash, file.NextLine = "included", body.fragmentHash, entry.nextOffset
		file.Ranges = []dtochat.TurnReferenceRange{{Start: 1, End: entry.includedLines}}
	}
	return file
}

// marshalToolInfo 对工具信息做稳定 JSON 编码，用于 schema 预算估算。
func marshalToolInfo(tool fantasy.AgentTool) ([]byte, error) {
	return json.Marshal(tool.Info())
}
