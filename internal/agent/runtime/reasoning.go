package agent

import (
	"fmt"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"

	"github.com/lyonmu/kaguya/internal/consts"
)

// reasoningProviderOptions 把模型记录的思考开关与强度映射为对应协议的 Fantasy ProviderOptions。
// 业务层只提供 reasoning_enabled 与 reasoning_effort，协议差异在这里消化：
//   - OpenAI Chat / Responses：开启时映射 minimal/low/medium/high/xhigh/max，关闭时显式传 none；
//   - Anthropic：开启时映射 low/medium/high/xhigh/max（Fantasy 据此使用 adaptive thinking），
//     关闭时不设置 Effort；Anthropic 不支持 minimal，遇到时明确报错而不是降级。
//
// 未启用思考时不校验强度；启用时遇到协议不支持的取值直接报错，不静默回退。
func reasoningProviderOptions(cfg ProviderConfig) (fantasy.ProviderOptions, error) {
	switch cfg.Protocol {
	case consts.ProtocolOpenAIChat:
		effort := openai.ReasoningEffortNone
		if cfg.ReasoningEnabled == consts.IsTrue {
			mapped, err := openAIReasoningEffort(cfg.ReasoningEffort)
			if err != nil {
				return nil, err
			}
			effort = mapped
		}
		return openai.NewProviderOptions(&openai.ProviderOptions{
			ReasoningEffort: openai.ReasoningEffortOption(effort),
		}), nil
	case consts.ProtocolOpenAIResponses:
		effort := openai.ReasoningEffortNone
		if cfg.ReasoningEnabled == consts.IsTrue {
			mapped, err := openAIReasoningEffort(cfg.ReasoningEffort)
			if err != nil {
				return nil, err
			}
			effort = mapped
		}
		return openai.NewResponsesProviderOptions(&openai.ResponsesProviderOptions{
			ReasoningEffort: openai.ReasoningEffortOption(effort),
		}), nil
	case consts.ProtocolAnthropic:
		if cfg.ReasoningEnabled != consts.IsTrue {
			return nil, nil
		}
		effort, err := anthropicReasoningEffort(cfg.ReasoningEffort)
		if err != nil {
			return nil, err
		}
		return anthropic.NewProviderOptions(&anthropic.ProviderOptions{Effort: &effort}), nil
	default:
		// 协议缺失或未知由 buildLanguageModel 报错，这里不追加任何选项。
		return nil, nil
	}
}

// openAIReasoningEffort 把业务强度映射为 OpenAI 协议的 ReasoningEffort。
func openAIReasoningEffort(effort consts.ReasoningEffort) (openai.ReasoningEffort, error) {
	switch effort {
	case consts.ReasoningEffortMinimal:
		return openai.ReasoningEffortMinimal, nil
	case consts.ReasoningEffortLow:
		return openai.ReasoningEffortLow, nil
	case consts.ReasoningEffortMedium:
		return openai.ReasoningEffortMedium, nil
	case consts.ReasoningEffortHigh:
		return openai.ReasoningEffortHigh, nil
	case consts.ReasoningEffortXHigh:
		return openai.ReasoningEffortXHigh, nil
	case consts.ReasoningEffortMax:
		return openai.ReasoningEffortMax, nil
	default:
		return "", fmt.Errorf("unsupported reasoning effort %q", effort)
	}
}

// anthropicReasoningEffort 把业务强度映射为 Anthropic 协议的 Effort。
// Anthropic 没有 minimal 等级，该取值会返回 unsupported 错误。
func anthropicReasoningEffort(effort consts.ReasoningEffort) (anthropic.Effort, error) {
	switch effort {
	case consts.ReasoningEffortLow:
		return anthropic.EffortLow, nil
	case consts.ReasoningEffortMedium:
		return anthropic.EffortMedium, nil
	case consts.ReasoningEffortHigh:
		return anthropic.EffortHigh, nil
	case consts.ReasoningEffortXHigh:
		return anthropic.EffortXHigh, nil
	case consts.ReasoningEffortMax:
		return anthropic.EffortMax, nil
	default:
		return "", fmt.Errorf("unsupported reasoning effort %q", effort)
	}
}
