package system

import "github.com/lyonmu/kaguya/internal/ent"

type SystemAccessLogResp struct {
	ID                   string `json:"id"`                               // ID
	AccessIP             string `json:"access_ip"`                        // 访问IP
	AccessTime           int64  `json:"access_time"`                      // 操作时间
	Os                   string `json:"os,omitempty"`                     // 操作系统
	Platform             string `json:"platform,omitempty"`               // 操作平台
	BrowserName          string `json:"browser_name,omitempty"`           // 浏览器名称
	BrowserVersion       string `json:"browser_version,omitempty"`        // 浏览器版本
	BrowserEngineName    string `json:"browser_engine_name,omitempty"`    // 浏览器引擎名称
	BrowserEngineVersion string `json:"browser_engine_version,omitempty"` // 浏览器引擎版本
}

func (r *SystemAccessLogResp) LoadDb(e *ent.KaguyaAccessLog) {
	r.ID = e.ID
	r.AccessIP = e.AccessIP
	r.AccessTime = e.AccessTime
	r.Os = e.Os
	r.Platform = e.Platform
	r.BrowserName = e.BrowserName
	r.BrowserVersion = e.BrowserVersion
	r.BrowserEngineName = e.BrowserEngineName
	r.BrowserEngineVersion = e.BrowserEngineVersion
}

type SystemAccessLogListResp struct {
	Total    int                    `json:"total,omitempty"`     // 总条数
	Items    []*SystemAccessLogResp `json:"items,omitempty"`     // 操作日志列表
	Page     int                    `json:"page,omitempty"`      // 页码
	PageSize int                    `json:"page_size,omitempty"` // 每页条数
}
