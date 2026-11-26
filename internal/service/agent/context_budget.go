package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"charm.land/fantasy"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
)

// contextBudgetInput 是一次轮次预算计算的纯输入；所有 token 数值都是估算，
// 不是模型专属 tokenizer。system 包含完整全局指令，预算不得为引用内容截断它。
type contextBudgetInput struct {
	window            int64
	compactionPercent int
	systemTokens      int64 // system prompt（含全局指令与工具说明）
	toolSchemaTokens  int64 // 工具 schema
	historyTokens     int64 // 需要保留的历史
	userTokens        int64 // 本轮用户输入（含内联引用）
	memoryTokens      int64 // memory sidecar
	outputReserve     int64 // 生成预留
}

// contextBudget 是纯计算结果：window 未知时 known=false，不伪造容量。
type contextBudget struct {
	known           bool
	window          int64
	threshold       int64 // 触发压缩的输入占用阈值
	remaining       int64 // 阈值内剩余可用预算
	referenceBudget int64 // 引用行内内容最多可用的预算
}

// computeContextBudget 统一 system/tools/history/user/memory/output 的预算口径。
// 引用内容最多使用剩余预算的 25%，其余留给后续工具 step；global/system 不参与裁剪。
func computeContextBudget(input contextBudgetInput) contextBudget {
	budget := contextBudget{known: input.window > 0, window: input.window}
	if !budget.known {
		return budget
	}
	budget.threshold = contextInputLimit(input.window, input.compactionPercent, input.outputReserve)
	used := input.systemTokens + input.toolSchemaTokens + input.historyTokens + input.userTokens + input.memoryTokens
	budget.remaining = max(budget.threshold-used, 0)
	budget.referenceBudget = budget.remaining * 25 / 100
	return budget
}

// 输出预留占用完整模型窗口，输入同时受压缩比例与完整窗口的剩余容量约束。
func contextInputLimit(window int64, percent int, outputReserve int64) int64 {
	threshold := window * int64(compactionPercent(percent)) / 100
	return max(0, min(threshold, window-outputReserve))
}

// contextFingerprint 标识影响历史基线有效性的上下文特征。任一变化都必须
// 废弃上一轮 context_tokens 基线并保守重估，不能只比较模型名。
type contextFingerprint struct {
	modelKey           string
	providerKey        string
	configurationHash  string
	systemHash         string
	instructionFormat  string
	permissionMode     string
	instructionHash    string
	instructionVersion int64
	toolSchemaHash     string
	memorySelectionKey string
	baselineUnknown    bool
}

const contextFingerprintVersion = 2

// 进程内指令 Version 用于排障，不作为跨重启时钟；只比较实际内容与配置。
func (f contextFingerprint) equal(other contextFingerprint) bool { return f.digest() == other.digest() }

func (f contextFingerprint) digest() string {
	// 不序列化 provider/model 整条记录，尤其不能带入 APIKey。
	data, _ := json.Marshal(struct {
		Model, Provider, Configuration, System, Instructions, Format, Tools, Memory, Permission string
		BaselineUnknown                                                                         bool
	}{f.modelKey, f.providerKey, f.configurationHash, f.systemHash, f.instructionHash, f.instructionFormat, f.toolSchemaHash, f.memorySelectionKey, f.permissionMode, f.baselineUnknown})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// toolSchemaFingerprint 保留纯 schema helper；生产入口同时消费可复用标记和错误。
func toolSchemaFingerprint(tools []fantasy.AgentTool) string {
	hash, _, err := effectiveToolsFingerprint(tools)
	if err != nil {
		return ""
	}
	return hash
}

// effectiveToolsFingerprint 不编码未知 provider options：当前工厂只有 nil/空 options。
// 不支持的非空 options 保持实际执行语义，只禁止复用实际 token 基线。
func effectiveToolsFingerprint(tools []fantasy.AgentTool) (string, bool, error) {
	type entry struct {
		Identity string
		Info     fantasy.ToolInfo
	}
	entries := make([][]byte, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	reusable := true
	for _, item := range tools {
		if item == nil {
			continue
		}
		info := item.Info()
		if seen[info.Name] {
			return "", false, fmt.Errorf("duplicate effective tool name")
		}
		seen[info.Name] = true
		identity := "unknown:" + info.Name
		if source, ok := item.(toolIdentityProvider); ok && source.PermissionIdentity() != "" {
			identity = source.PermissionIdentity()
		}
		if len(item.ProviderOptions()) != 0 {
			reusable = false
		}
		data, err := json.Marshal(entry{Identity: identity, Info: info})
		if err != nil {
			return "", false, fmt.Errorf("cannot fingerprint effective tool schema")
		}
		entries = append(entries, data)
	}
	sort.Slice(entries, func(i, j int) bool { return string(entries[i]) < string(entries[j]) })
	hasher := sha256.New()
	for _, data := range entries {
		hasher.Write(data)
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil)), reusable, nil
}

// fingerprintURL 只投影 URL 的非秘密请求身份。未知 query 不进入 hash，禁止复用。
// Parse 错误不携带原始 URL，避免凭据出现在错误链中。
func fingerprintURL(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("cannot fingerprint request location")
	}
	data, _ := json.Marshal(struct{ Scheme, Host, Path string }{
		strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host), parsed.EscapedPath(),
	})
	return fullTextFingerprint(string(data)), parsed.RawQuery == "" && !parsed.ForceQuery, nil
}

func fullTextFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// textFingerprint 对系统/指令文本做稳定哈希，用于指令版本变化时失效旧基线。
func textFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// memorySelectionFingerprint 把本轮冻结召回选择编码为稳定哈希；
// 选择变化时旧 context_tokens 基线不再适用。
func memorySelectionFingerprint(selection *dtomemory.TurnMemorySelection) string {
	if selection == nil {
		return ""
	}
	data, err := json.Marshal(selection)
	if err != nil {
		return ""
	}
	return fullTextFingerprint(string(data))
}
