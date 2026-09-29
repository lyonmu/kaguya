package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	codingtools "github.com/lyonmu/kaguya/internal/agent/tools"
	"github.com/lyonmu/kaguya/internal/global"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
	"go.uber.org/zap"
)

// projectTools never trusts a continuation request's project_id. A saved
// conversation's database association is the sole authority for its workspace.
// 普通对话（无项目）使用 ~/.kaguya 作为工作目录，拥有相同的 7 个内置工具。
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

	var workDir string
	var logger *zap.Logger

	if projectID != "" {
		// 项目对话：使用项目工作区
		path, err := (&projectsvc.ProjectSvc{}).Workspace(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("project workspace unavailable: %w", err)
		}
		workDir = path
		logger = global.Logger.With(zap.String("conversation_id", conversationID), zap.String("project_id", projectID))
	} else {
		// 普通对话：使用 ~/.kaguya 作为工作目录
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get user home directory: %w", err)
		}
		workDir = filepath.Join(homeDir, ".kaguya")
		// 确保目录存在
		if err := os.MkdirAll(workDir, 0755); err != nil {
			return nil, fmt.Errorf("create conversation workspace: %w", err)
		}
		logger = global.Logger.With(zap.String("conversation_id", conversationID), zap.String("workspace", "user_home"))
	}

	set, err := codingtools.New(workDir, conversationID, logger)
	if err != nil {
		return nil, err
	}

	if projectID != "" {
		global.Logger.Debug("project coding tools registered", zap.String("conversation_id", conversationID), zap.String("project_id", projectID), zap.String("workspace", workDir), zap.Strings("tools", []string{"read", "bash", "edit", "write", "grep", "find", "ls"}))
	} else {
		global.Logger.Debug("conversation coding tools registered", zap.String("conversation_id", conversationID), zap.String("workspace", workDir), zap.Strings("tools", []string{"read", "bash", "edit", "write", "grep", "find", "ls"}))
	}

	return set, nil
}
