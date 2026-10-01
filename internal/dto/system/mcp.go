package system

import (
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"time"
)

type SystemMCPSaveReq = agentmcp.Config

type SystemMCPStateReq struct {
	Enabled *bool `json:"enabled" binding:"required"`
}

type SystemMCPPageReq struct {
	Name     string `form:"name"`
	Page     int    `form:"page,default=1" binding:"min=1"`
	PageSize int    `form:"page_size,default=10" binding:"min=1,max=100"`
}

type SystemMCPResp struct {
	agentmcp.Config
	ID     string          `json:"id"`
	Enabled bool           `json:"enabled"`
	Status agentmcp.Status `json:"status"`
	// Phase 是细粒度状态：stopped/connecting/running/backoff/circuit_open；
	// 旧 state 字段保持 running/stopped/error 兼容。
	Phase               string     `json:"phase"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	ReconnectCycle      int64      `json:"reconnect_cycle"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	CircuitOpenedAt     *time.Time `json:"circuit_opened_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type SystemMCPListResp struct {
	Items    []*SystemMCPResp `json:"items"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}
