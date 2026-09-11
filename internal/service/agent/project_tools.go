package agent

import (
	"context"
	"fmt"

	codingtools "github.com/lyonmu/kaguya/internal/agent/tools"
	"github.com/lyonmu/kaguya/internal/global"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
	"go.uber.org/zap"
)

// projectTools never trusts a continuation request's project_id. A saved
// conversation's database association is the sole authority for its workspace.
func (s *AgentSvc) projectTools(ctx context.Context, conversationID, requestedProject string, version int64) (*codingtools.Set, error) {
	projectID := requestedProject
	if version > 0 {
		conversation, err := s.ConversationDetail(ctx, conversationID)
		if err != nil {
			return nil, err
		}
		projectID = ""
		if conversation.ProjectID != nil {
			projectID = *conversation.ProjectID
		}
	}
	if projectID == "" {
		return nil, nil
	}
	path, err := (&projectsvc.ProjectSvc{}).Workspace(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project workspace unavailable: %w", err)
	}
	set, err := codingtools.New(path, conversationID, global.Logger.With(zap.String("conversation_id", conversationID), zap.String("project_id", projectID)))
	if err != nil {
		return nil, err
	}
	global.Logger.Info("project coding tools registered", zap.String("conversation_id", conversationID), zap.String("project_id", projectID), zap.Strings("tools", []string{"read", "bash", "edit", "write"}))
	return set, nil
}
