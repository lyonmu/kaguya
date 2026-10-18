package agent

import (
	"context"
	"encoding/json"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/agent/memorytools"
	codingtools "github.com/lyonmu/kaguya/internal/agent/tools"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// chatPrompt 是一次轮次使用的提示词与工具集合。
type chatPrompt struct {
	requestPrompt string              // 原始用户提问；引用只在首个 PrepareStep 展开
	system        string              // 系统提示词：全局人设 + 指令 + 工具说明
	tools         []fantasy.AgentTool // 项目工具 + MCP 工具 + 只读记忆工具
	instructions  string              // 本轮实际使用的完整指令文本
	memory        memoryTurn          // 本轮自动召回的临时上下文与冻结选择

	// instructionsFormat 为 split 或 legacy；split 下 projectSnapshot 非 nil 表示
	// 本轮成功后需要冻结项目指令（全局保持动态，不写入会话快照）。
	instructionsFormat string
	projectSnapshot    *string
	globalHash         string
	globalVersion      int64

	// 规范化选择不含文件正文，来源归属在准备阶段从数据库解析。
	references          []dtochat.Reference
	requestedReferences *dtochat.TurnReferenceSummary
	projectID           string
}

// prepareChatPrompt 组装系统提示词、项目指令快照、文件引用与可用工具。
// conversationID 用于指令快照；projectID 来自数据库权威项目归属，普通会话为空。
// 引用这里只校验形状/归属，不进行文件 I/O；history 的预算在首个 PrepareStep 计算。
func prepareChatPrompt(ctx context.Context, target *chatTarget, toolset *codingtools.Set, conversationID, projectID string, history []fantasy.Message, req *dtochat.ChatReq, mem memoryTurn) (*chatPrompt, error) {
	projectDir := ""
	if toolset != nil {
		projectDir = toolset.CWD()
	}
	instructions, err := conversationInstructions(ctx, conversationID, target.info.GlobalAgentsPaths, projectDir)
	if err != nil {
		return nil, err
	}
	basePrompt := ""
	if target.info.GlobalSystemPrompt != nil {
		basePrompt = *target.info.GlobalSystemPrompt
	}
	prompt := &chatPrompt{
		instructions: instructions.text, memory: mem,
		instructionsFormat: instructions.format, projectSnapshot: instructions.projectSnapshot,
		globalHash: instructions.globalHash, globalVersion: instructions.globalVersion,
		system: servicesystem.ChatSystemPrompt(basePrompt, target.info.SystemPrompt) + instructions.text,
	}
	if toolset != nil {
		toolset.SetCommandTimeout(time.Duration(*target.info.CommandTimeoutSeconds) * time.Second)
		prompt.tools = toolset.AllTools()
		prompt.system += "\n\n" + toolset.SystemPrompt()
	}

	mcpTools := agentmcp.Default.Tools()
	prompt.tools = append(prompt.tools, mcpTools...)
	// 工具名与外部 MCP 工具冲突时不注册记忆工具，保持已有工具契约不变。
	if mem.reader != nil && !toolNameConflict(mcpTools, "memory_search", "memory_read") {
		prompt.tools = append(prompt.tools, memorytools.Tools(mem.reader)...)
		prompt.system += "\n\n" + memorytools.SystemPrompt
	}
	refs, err := normalizeReferences(req.Files, req.References)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 && projectID == "" {
		return nil, ErrReferenceInvalid
	}
	prompt.requestPrompt = req.Messages
	prompt.references, prompt.projectID = refs, projectID
	prompt.requestedReferences, err = referenceIntent(refs)
	if err != nil {
		return nil, err
	}

	return prompt, nil
}

// toolNameConflict 检查记忆工具名与外部 MCP 工具的冲突。
func toolNameConflict(existing []fantasy.AgentTool, names ...string) bool {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	for _, tool := range existing {
		if tool != nil && wanted[tool.Info().Name] {
			global.Logger.Sugar().Warnf("memory tool name conflicts with MCP tool %q; memory tools not registered", tool.Info().Name)
			return true
		}
	}
	return false
}

// closeToolset 释放项目工作区句柄；失败只记录日志，不影响已完成的轮次。
func closeToolset(toolset *codingtools.Set) {
	if err := toolset.Close(); err != nil {
		global.Logger.Sugar().Warnf("close project tool workspace: %v", err)
	}
}

// referenceIntent 保存选择而非文件读取证明；正文、显示名和文件状态不进入意图摘要。

func referenceIntent(refs []dtochat.Reference) (*dtochat.TurnReferenceSummary, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	summary := &dtochat.TurnReferenceSummary{PreparationState: "requested"}
	size := len(`{"preparation_state":"requested","sources":[]}`)
	for _, ref := range refs {
		source := dtochat.TurnReferenceSource{Type: ref.Type, Path: ref.Path}
		encoded, err := json.Marshal(source)
		if err != nil {
			return nil, ErrReferenceInvalid
		}
		size += len(encoded) + 1
		if size > referenceMetadataBytes {
			return nil, ErrContextBudgetExceeded
		}
		summary.Sources = append(summary.Sources, source)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > referenceMetadataBytes {
		return nil, ErrContextBudgetExceeded
	}
	return summary, nil
}
