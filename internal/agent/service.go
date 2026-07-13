package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/lyonmu/kaguya/internal/agent/conversation"
	"github.com/lyonmu/kaguya/internal/agent/usage"
)

// Request represents a single turn in a conversation.
type Request struct {
	ConversationID string
	MessageID      string
	Prompt         string
}

// Result is the outcome of a service Generate call.
type Result struct {
	ConversationID string
	Text           string
	Usage          usage.NormalizedUsage
}

// Service orchestrates conversation history with agent execution.
type Service struct {
	agent fantasy.Agent
	store conversation.Store
}

// NewService creates a Service that orchestrates the given agent and store.
func NewService(agent fantasy.Agent, store conversation.Store) (*Service, error) {
	if agent == nil {
		return nil, errors.New("agent is required")
	}
	if store == nil {
		return nil, errors.New("store is required")
	}
	return &Service{agent: agent, store: store}, nil
}

// Generate loads conversation history, runs the agent, and persists all
// resulting messages (user prompt, tool calls, tool results, assistant
// text) back into the store.
func (s *Service) Generate(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return Result{}, errors.New("conversation ID is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return Result{}, errors.New("prompt is required")
	}

	history, err := s.store.Load(ctx, req.ConversationID)
	if err != nil {
		return Result{}, fmt.Errorf("load conversation: %w", err)
	}

	ctx = usage.WithConversationID(ctx, req.ConversationID)
	ctx = usage.WithMessageID(ctx, req.MessageID)

	result, err := s.agent.Generate(ctx, fantasy.AgentCall{
		Prompt:   req.Prompt,
		Messages: history,
	})
	if err != nil {
		return Result{}, fmt.Errorf("generate response: %w", err)
	}

	messages := append([]fantasy.Message{fantasy.NewUserMessage(req.Prompt)}, flattenStepMessages(result.Steps)...)
	if err := s.store.Append(ctx, req.ConversationID, messages...); err != nil {
		return Result{}, fmt.Errorf("store messages: %w", err)
	}

	return Result{
		ConversationID: req.ConversationID,
		Text:           result.Response.Content.Text(),
		Usage:          usage.FromFantasyUsage(result.TotalUsage),
	}, nil
}

// flattenStepMessages sequentially appends all StepResult.Messages from every
// step, preserving tool-call and tool-result pairs for future turns.
func flattenStepMessages(steps []fantasy.StepResult) []fantasy.Message {
	var out []fantasy.Message
	for _, step := range steps {
		out = append(out, step.Messages...)
	}
	return out
}
