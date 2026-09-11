package project

// GitFile 表示工作区中一个相对项目根的变更文件。
type GitFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Staged    bool   `json:"staged"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type GitStatusResp struct {
	IsGit     bool      `json:"is_git"`
	Message   string    `json:"message,omitempty"`
	Branch    string    `json:"branch"`
	Head      string    `json:"head"`
	Files     []GitFile `json:"files"`
	Truncated bool      `json:"truncated"`
}

type GitDiffReq struct {
	Path string `form:"path" binding:"required,max=4096"`
}

type GitDiffResp struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Diff      string `json:"diff"`
}
