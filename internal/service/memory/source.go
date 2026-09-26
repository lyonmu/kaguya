package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// ProjectionVersion 是来源投影的格式版本，随 source_key 后缀一起防止重复入队。
const ProjectionVersion = 1

// 来源投影的有界参数：单段与单来源总量限制，超长按可追溯 segment 切分，
// 不截掉末尾后假装全部处理成功。
const (
	maxSegmentChars = 6000
	maxSourceChars  = 24000
	maxToolObsChars = 2000
)

// toolObservationWhitelist 是允许进入来源投影的工具观察白名单；
// 只读观察工具的成功结果可作为 tool_observation 证据，bash 等命令输出不进入记忆。
var toolObservationWhitelist = map[string]bool{"read": true, "grep": true, "ls": true, "find": true}

// Segment 是投影内的可见片段；origin 区分用户陈述、助手断言与工具观察。
type Segment struct {
	PartKey string `json:"part_key"`
	Origin  string `json:"origin"`
	Text    string `json:"text"`
}

// Segment origins；助手断言不能独立使事实变为 verified。
const (
	OriginUserStatement      = "user_statement"
	OriginAssistantAssertion = "assistant_assertion"
	OriginToolObservation    = "tool_observation"
	OriginDocumentStatement  = "document_statement"
)

// SourceProjection 是后台提炼使用的有界、版本化可见来源投影。
// 不使用 messages / context_messages，也不遍历临时 Bash 输出文件。
type SourceProjection struct {
	SourceID       string    `json:"source_id"`
	ScopeKey       string    `json:"scope_key"`
	SourceTime     string    `json:"source_time"`
	ConversationID string    `json:"conversation_id"`
	TurnID         string    `json:"turn_id"`
	TurnStatus     string    `json:"turn_status"`
	FinishReason   string    `json:"finish_reason"`
	PartsTotal     int       `json:"parts_total"` // 从游标开始的总分段数，用于判定覆盖范围
	Segments       []Segment `json:"segments"`
}

// ProjectionHash 是投影片段集合的稳定内容哈希。
func ProjectionHash(segments []Segment) string {
	h := sha256.New()
	for _, s := range segments {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", s.PartKey, s.Origin, s.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// redactionPatterns 覆盖已知秘密模式；正则脱敏不能保证识别所有秘密，
// 高风险资料应排除或要求用户确认，不宣传为可放心自动上传所有聊天。
var redactionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(sk|rk|api|ak|ghp|xoxb|xoxp|pat)[-_][A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\b(bearer|basic|token|api[-_ ]?key|apikey|secret|password|passwd|pwd)\b\s*[:=]\s*\S+`),
	regexp.MustCompile(`\benc:v2:\S+`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
}

const redactedMark = "[REDACTED]"

// redactSecrets 在发送任务模型之前替换已知秘密模式。
func redactSecrets(text string) string {
	for _, pattern := range redactionPatterns {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			// 保留 key 名便于理解，替换取值部分。
			if index := strings.IndexAny(match, ":="); index >= 0 && index < len(match)-1 {
				return match[:index+1] + redactedMark
			}
			return redactedMark
		})
	}
	return text
}

// splitSegments 把逻辑片段切成有界分段：单段时保留原 part_key，
// 多段时追加分段序号，保证证据 part_key 可追溯。
func splitSegments(partKey, origin, text string) []Segment {
	text = redactSecrets(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var pieces []string
	runes := []rune(text)
	for start := 0; start < len(runes); start += maxSegmentChars {
		pieces = append(pieces, string(runes[start:min(start+maxSegmentChars, len(runes))]))
	}
	if len(pieces) == 1 {
		return []Segment{{PartKey: partKey, Origin: origin, Text: pieces[0]}}
	}
	out := make([]Segment, 0, len(pieces))
	for i, piece := range pieces {
		out = append(out, Segment{PartKey: fmt.Sprintf("%s#%d", partKey, i), Origin: origin, Text: piece})
	}
	return out
}

// BuildTurnSegments 从已提交轮次构造可见片段：用户提问与 type=text 的展示块，
// 外加白名单、已配对完成、大小受限的工具观察。reasoning/签名/私有 metadata 不进入投影。
func BuildTurnSegments(userContent string, blocks []*ent.KaguyaChatBlock) []Segment {
	var segments []Segment
	segments = append(segments, splitSegments("user", OriginUserStatement, userContent)...)
	for _, b := range blocks {
		switch b.Type {
		case kaguyachatblock.TypeText:
			segments = append(segments, splitSegments(
				fmt.Sprintf("assistant:block-%d", b.Sequence), OriginAssistantAssertion, b.Text)...)
		case kaguyachatblock.TypeToolCall:
			// 只允许白名单、已配对完成（结果块已落库）、大小受限的成功结果片段。
			if !toolObservationWhitelist[b.ToolName] || b.ToolCallID == "" || b.IsError || len(b.Output) == 0 {
				continue
			}
			var output dtochat.ToolOutput
			if err := json.Unmarshal(b.Output, &output); err != nil || output.Type != dtochat.ToolOutputText {
				continue
			}
			// is_error=false 只能证明该工具没有按协议报告错误，不能泛化成方案已正确。
			segments = append(segments, splitSegments(
				"tool:"+b.ToolCallID, OriginToolObservation,
				truncateRunes(output.Text, maxToolObsChars))...)
		}
	}
	return segments
}

// BuildNoteSegments 是用户笔记的投影；笔记正文即用户陈述。
func BuildNoteSegments(body string) []Segment {
	return splitSegments("note", OriginUserStatement, body)
}

// BuildDocumentSegments 是显式导入资料的投影；资料陈述不自动等于外部事实已核实。
func BuildDocumentSegments(content string) []Segment {
	return splitSegments("document", OriginDocumentStatement, content)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n[truncated]"
}

// LoadSourceSegments 重建单个来源的完整片段集合。
// turn 来源从轮次行重建；note 来源从其页面正文重建；import 来源读取导入快照。
func LoadSourceSegments(ctx context.Context, client *ent.Client, src *ent.KaguyaMemorySource) ([]Segment, error) {
	segments, _, _, err := loadSourceContent(ctx, client, src)
	return segments, err
}

// deriveSourceHash 在后台首次处理来源时补全内容哈希；捕获事务不读投影，
// 所有可异步推导的数据都放在 Worker 阶段。内容不变的来源重复处理保持幂等。
func deriveSourceHash(ctx context.Context, client *ent.Client, src *ent.KaguyaMemorySource, segments []Segment) error {
	hash := ProjectionHash(segments)
	if src.ContentHash == hash {
		return nil
	}
	if err := client.KaguyaMemorySource.UpdateOneID(src.ID).SetContentHash(hash).Exec(ctx); err != nil {
		return err
	}
	src.ContentHash = hash
	return nil
}

// loadSourceContent 重建片段并携带轮次状态与结束原因（step_limit 不代表任务完成）。
func loadSourceContent(ctx context.Context, client *ent.Client, src *ent.KaguyaMemorySource) ([]Segment, string, string, error) {
	switch src.Kind {
	case kaguyamemorysource.KindNote:
		pageID := strings.TrimPrefix(src.SourceKey, "note:")
		page, err := client.KaguyaMemoryPage.Query().Where(kaguyamemorypage.IDEQ(pageID)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, "", "", nil
		}
		if err != nil {
			return nil, "", "", err
		}
		return BuildNoteSegments(page.Body), "", "", nil
	case kaguyamemorysource.KindImport:
		// 导入快照与来源同事务落库；文件后续变化不回写历史证据。
		return BuildDocumentSegments(src.RawContent), "", "", nil
	default:
		if src.TurnID == "" {
			return nil, "", "", fmt.Errorf("memory source %q has no turn", src.ID)
		}
		turn, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.IDEQ(src.TurnID)).
			WithBlocks(func(q *ent.KaguyaChatBlockQuery) { q.Order(kaguyachatblock.BySequence()) }).
			Only(ctx)
		if err != nil {
			return nil, "", "", err
		}
		return BuildTurnSegments(turn.UserContent, turn.Edges.Blocks), string(turn.Status), turn.FinishReason, nil
	}
}

// LoadSourceProjection 从游标开始构造完整有界投影（含轮次状态与结束原因）。
func LoadSourceProjection(ctx context.Context, client *ent.Client, src *ent.KaguyaMemorySource) (*SourceProjection, error) {
	segments, turnStatus, finishReason, err := loadSourceContent(ctx, client, src)
	if err != nil {
		return nil, err
	}
	projection := BuildSourceProjection(src, segments)
	projection.TurnStatus = turnStatus
	projection.FinishReason = finishReason
	return projection, nil
}

// BuildSourceProjection 从游标开始构造有界投影；PartsTotal 记录覆盖范围，
// 剩余分段仍待处理，不能把部分覆盖的来源标成 processed。
func BuildSourceProjection(src *ent.KaguyaMemorySource, segments []Segment) *SourceProjection {
	remaining := segments[min(src.CursorPart, len(segments)):]
	projection := &SourceProjection{
		SourceID:       src.ID,
		ScopeKey:       src.ScopeKey,
		SourceTime:     src.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"),
		ConversationID: src.ConversationID,
		TurnID:         src.TurnID,
		PartsTotal:     len(remaining),
		Segments:       []Segment{},
	}
	total := 0
	for _, segment := range remaining {
		if total+len(segment.Text) > maxSourceChars {
			break
		}
		total += len(segment.Text)
		projection.Segments = append(projection.Segments, segment)
	}
	return projection
}

// ProjectionPayload 是冻结输入的序列化形式；input_hash 对其计算。
func ProjectionPayload(projections []*SourceProjection) ([]byte, error) {
	data, err := json.Marshal(projections)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCount(data) == 0 {
		return nil, fmt.Errorf("empty memory projection payload")
	}
	return data, nil
}
