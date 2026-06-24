package usage

import (
	"context"
	"time"

	"charm.land/fantasy"
)

type AgentOption func(*Agent)

type Agent struct {
	inner    fantasy.Agent
	recorder UsageRecorder

	provider string
	model    string

	conversationIDFunc func(context.Context) string
	messageIDFunc      func(context.Context) string
}

func NewAgent(inner fantasy.Agent, recorder UsageRecorder, opts ...AgentOption) *Agent {
	a := &Agent{
		inner:    inner,
		recorder: recorder,
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

func WithProvider(provider string) AgentOption {
	return func(a *Agent) {
		a.provider = provider
	}
}

func WithModel(model string) AgentOption {
	return func(a *Agent) {
		a.model = model
	}
}

func WithConversationIDFunc(fn func(context.Context) string) AgentOption {
	return func(a *Agent) {
		a.conversationIDFunc = fn
	}
}

func WithMessageIDFunc(fn func(context.Context) string) AgentOption {
	return func(a *Agent) {
		a.messageIDFunc = fn
	}
}

func (a *Agent) Generate(ctx context.Context, call fantasy.AgentCall) (*fantasy.AgentResult, error) {
	startedAt := time.Now()

	result, err := a.inner.Generate(ctx, call)

	a.record(ctx, "generate", startedAt, result, err)

	return result, err
}

func (a *Agent) Stream(ctx context.Context, call fantasy.AgentStreamCall) (*fantasy.AgentResult, error) {
	startedAt := time.Now()

	result, err := a.inner.Stream(ctx, call)

	a.record(ctx, "stream", startedAt, result, err)

	return result, err
}

func (a *Agent) record(
	ctx context.Context,
	mode string,
	startedAt time.Time,
	result *fantasy.AgentResult,
	err error,
) {
	if a.recorder == nil {
		return
	}

	finishedAt := time.Now()

	turn := TurnUsage{
		Provider:   a.provider,
		Model:      a.model,
		Mode:       mode,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Latency:    finishedAt.Sub(startedAt),
	}

	if a.conversationIDFunc != nil {
		turn.ConversationID = a.conversationIDFunc(ctx)
	}

	if a.messageIDFunc != nil {
		turn.MessageID = a.messageIDFunc(ctx)
	}

	if err != nil {
		turn.Err = err.Error()
	}

	if result != nil {
		turn.Total = FromFantasyUsage(result.TotalUsage)

		if len(result.Steps) > 0 {
			turn.Steps = make([]StepUsage, 0, len(result.Steps))

			for i, step := range result.Steps {
				turn.Steps = append(turn.Steps, StepUsage{
					StepIndex: i + 1,
					Usage:     FromFantasyUsage(step.Response.Usage),
				})
			}
		}
	}

	_ = a.recorder.RecordUsage(ctx, turn)
}
