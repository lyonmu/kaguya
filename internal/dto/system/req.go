package system

type SystemAccessLogReq struct {
	AccessIP             string `json:"access_ip,omitempty"`              // 访问IP
	AccessTime           int64  `json:"access_time,omitempty"`            // 操作时间
	Os                   string `json:"os,omitempty"`                     // 操作系统
	Platform             string `json:"platform,omitempty"`               // 操作平台
	BrowserName          string `json:"browser_name,omitempty"`           // 浏览器名称
	BrowserVersion       string `json:"browser_version,omitempty"`        // 浏览器版本
	BrowserEngineName    string `json:"browser_engine_name,omitempty"`    // 浏览器引擎名称
	BrowserEngineVersion string `json:"browser_engine_version,omitempty"` // 浏览器引擎版本
}

type SystemAccessLogPageReq struct {
	AccessIP  string `json:"access_ip,omitempty"`                                                                                              // 访问IP
	StartTime int64  `json:"start_time,omitempty" form:"start_time"`                                                                           // 开始时间
	EndTime   int64  `json:"end_time,omitempty" form:"end_time"`                                                                               // 结束时间
	Page      int    `json:"page,omitempty" binding:"required,min=1" form:"page" minimum:"1" default:"1"`                                      // 页码
	PageSize  int    `json:"page_size,omitempty" binding:"required,min=10,max=1000" form:"page_size" minimum:"10" maximum:"1000" default:"10"` // 每页条数
}
