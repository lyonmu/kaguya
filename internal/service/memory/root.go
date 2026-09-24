// Package memory 实现可溯源的长期记忆：来源 outbox、后台任务模型增量编译的
// Wiki 页面、FTS5 本地召回与预算内上下文注入。设计依据 docs/memory-design.md。
package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproject"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

// 作用域只有三个固定取值；项目作用域复用项目 ID，但不接受请求或模型给出的归属。
const (
	ScopePersonal      = "personal"
	ScopeShared        = "shared"
	scopeProjectPrefix = "project:"
)

// CompilerVersion 是提炼/合并提示词与 JSON 契约的联合版本，记录到每次 Job。
const CompilerVersion = "1"

// TaskModelResolver 在每次执行尝试开始时解析任务模型调用快照。
type TaskModelResolver func(ctx context.Context, client *ent.Client, conversationID string) (*servicesystem.TaskModel, error)

// ErrConversationGone 表示会话已删除，其来源与召回同时失效。
var ErrConversationGone = errors.New("conversation is deleted")

// Service 捕获 client 与 logger；所有数据库操作显式使用该 client，
// 后台任务不依赖可变全局状态。
type Service struct {
	client     *ent.Client
	logger     *zap.Logger
	caller     ModelCaller
	taskModels TaskModelResolver
}

// NewService 装配记忆服务；模型调用与任务模型解析默认走真实实现，可由测试覆盖。
func NewService(client *ent.Client, logger *zap.Logger) *Service {
	return &Service{
		client:     client,
		logger:     logger,
		caller:     RuntimeCaller{},
		taskModels: servicesystem.ResolveTaskModel,
	}
}

// WithCaller 注入模型调用实现（测试用）。
func (s *Service) WithCaller(caller ModelCaller) *Service {
	s.caller = caller
	return s
}

// WithTaskModelResolver 注入任务模型解析（测试用）。
func (s *Service) WithTaskModelResolver(resolve TaskModelResolver) *Service {
	s.taskModels = resolve
	return s
}

// Policy 是记忆策略快照；epoch 是并发栅栏，不是授权本身。
type Policy struct {
	Enabled       bool // 全局可用开关
	AutoCapture   bool // 是否自动产生新来源（已考虑 Enabled）
	ContextTokens int  // 每轮注入上限（估算 token）
	Epoch         int64
}

// LoadPolicy 读取全局记忆策略。
func LoadPolicy(ctx context.Context, client *ent.Client) (Policy, error) {
	row, err := client.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldMemoryEnabled, kaguyasysteminfo.FieldMemoryAutoCapture,
			kaguyasysteminfo.FieldMemoryContextTokens, kaguyasysteminfo.FieldMemoryPolicyEpoch).
		Only(ctx)
	if err != nil {
		return Policy{}, err
	}
	return Policy{
		Enabled:       row.MemoryEnabled,
		AutoCapture:   row.MemoryEnabled && row.MemoryAutoCapture,
		ContextTokens: row.MemoryContextTokens,
		Epoch:         row.MemoryPolicyEpoch,
	}, nil
}

// BumpPolicyEpoch 在隐私模式变化、删除记忆/来源、项目删除或换绑时单调递增策略版本，
// 使在途提案不能再发布。调用方必须在同事务内调用。
func BumpPolicyEpoch(ctx context.Context, client *ent.Client) error {
	return client.KaguyaSystemInfo.Update().
		Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		AddMemoryPolicyEpoch(1).Exec(ctx)
}

// MemoryMode 是会话级记忆模式：inherit 继承全局 / off 关闭 / readonly 只读。
type MemoryMode string

const (
	ModeInherit  MemoryMode = "inherit"
	ModeOff      MemoryMode = "off"
	ModeReadOnly MemoryMode = "readonly"
)

// ConversationPolicy 是一次会话的有效记忆状态与项目归属。
type ConversationPolicy struct {
	Mode      MemoryMode
	ProjectID string
	Capture   bool // 允许自动捕获新来源
	Recall    bool // 允许自动召回与记忆工具
}

// ResolveConversationPolicy 合并全局策略与会话记忆模式；
// 私密/不记忆会话同时避免自动捕获和自动召回。
func ResolveConversationPolicy(ctx context.Context, client *ent.Client, conversationID string, policy Policy) (ConversationPolicy, error) {
	result := ConversationPolicy{Mode: ModeInherit}
	if conversationID == "" {
		result.Capture, result.Recall = policy.AutoCapture, policy.Enabled
		return result, nil
	}
	row, err := client.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(conversationID)).
		Select(kaguyaconversation.FieldMemoryMode, kaguyaconversation.FieldProjectID, kaguyaconversation.FieldDeletedAt).
		Only(ctx)
	if err != nil {
		return ConversationPolicy{}, err
	}
	if row.DeletedAt != nil {
		return ConversationPolicy{}, ErrConversationGone
	}
	if row.ProjectID != nil {
		result.ProjectID = *row.ProjectID
	}
	result.Mode = MemoryMode(row.MemoryMode)
	switch result.Mode {
	case ModeOff:
	case ModeReadOnly:
		result.Recall = policy.Enabled
	default:
		result.Capture, result.Recall = policy.AutoCapture, policy.Enabled
	}
	return result, nil
}

// ScopeKey 把数据库中的会话项目归属映射为作用域；空归属是个人作用域。
func ScopeKey(projectID string) string {
	if projectID == "" {
		return ScopePersonal
	}
	return scopeProjectPrefix + projectID
}

// RecallScopes 返回一次对话允许读取的作用域集合：
// 项目对话读取项目作用域与 shared，普通对话读取 personal 与 shared；
// 项目对话不默认读取个人记忆，任何对话都不会跨项目读取。
func RecallScopes(projectID string) []string {
	if projectID == "" {
		return []string{ScopePersonal, ScopeShared}
	}
	return []string{scopeProjectPrefix + projectID, ScopeShared}
}

// ValidateScope 校验人工写入的作用域：项目作用域必须指向未删除项目，
// 且项目删除后的历史不会重新归入 personal/shared。
func ValidateScope(ctx context.Context, client *ent.Client, scope string) error {
	switch {
	case scope == ScopePersonal || scope == ScopeShared:
		return nil
	case strings.HasPrefix(scope, scopeProjectPrefix):
		projectID := strings.TrimPrefix(scope, scopeProjectPrefix)
		if projectID == "" {
			return fmt.Errorf("%w: invalid memory scope %q", ErrPlanInvalid, scope)
		}
		exists, err := client.KaguyaProject.Query().
			Where(kaguyaproject.IDEQ(projectID), kaguyaproject.DeletedAtIsNil()).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: memory scope %q is not an active project", ErrPlanInvalid, scope)
		}
		return nil
	default:
		return fmt.Errorf("%w: invalid memory scope %q", ErrPlanInvalid, scope)
	}
}

// wake 唤醒后台 Worker；通知丢失也不丢任务，定时扫描和下次启动可发现待处理来源。
var wake = make(chan struct{}, 1)

// nowTime 统一时间取值。
func nowTime() time.Time { return time.Now() }

// Notify 唤醒记忆 Worker 立即重新扫描；重复通知是幂等的。
func Notify() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// pendingSourceCount 供状态接口展示待处理规模。
func pendingSourceCount(ctx context.Context, client *ent.Client) (int, error) {
	return client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.StateIn(
			kaguyamemorysource.StatePending, kaguyamemorysource.StateClaimed, kaguyamemorysource.StateFailed,
		)).Count(ctx)
}
