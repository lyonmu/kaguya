package project

// TreeItem 是项目文件树节点，路径始终相对项目根。
type TreeItem struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"is_dir"`
	Size     int64       `json:"size"`
	Children []*TreeItem `json:"children,omitempty"`
}

type TreeResp struct {
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	Items     []*TreeItem `json:"items"`
	Truncated bool        `json:"truncated"`
}

type ContentReq struct {
	Path string `form:"path" binding:"required,max=4096"`
}

type ContentResp struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}
