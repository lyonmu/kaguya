package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

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
	threshold       int64 // 触发压缩的占用阈值
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
	percent := compactionPercent(input.compactionPercent)
	budget.threshold = input.window * int64(percent) / 100
	used := input.systemTokens + input.toolSchemaTokens + input.historyTokens + input.userTokens + input.memoryTokens + input.outputReserve
	budget.remaining = max(budget.threshold-used, 0)
	budget.referenceBudget = budget.remaining * 25 / 100
	return budget
}

// contextFingerprint 标识影响历史基线有效性的上下文特征。任一变化都必须
// 废弃上一轮 context_tokens 基线并保守重估，不能只比较模型名。
type contextFingerprint struct {
	modelKey           string
	instructionHash    string
	instructionVersion int64
	toolSchemaHash     string
	memorySelectionKey string
}

func (f contextFingerprint) equal(other contextFingerprint) bool { return f == other }

// toolSchemaFingerprint 对工具名与 schema 做稳定哈希；工具集合或参数结构变化时
// 旧基线不再可信。排序保证与注册顺序无关。
func toolSchemaFingerprint(tools []fantasy.AgentTool) string {
	type entry struct {
		name string
		info any
	}
	entries := make([]entry, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		entries = append(entries, entry{name: item.Info().Name, info: item.Info()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	hasher := sha256.New()
	encoder := json.NewEncoder(hasher)
	for _, item := range entries {
		_ = encoder.Encode(item.name)
		_ = encoder.Encode(item.info)
	}
	return hex.EncodeToString(hasher.Sum(nil)[:8])
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
	return textFingerprint(string(data))
}
