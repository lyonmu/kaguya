package project

import "time"

type IDReq struct {
	ID string `uri:"id" binding:"required,max=64"`
}
type SaveReq struct {
	Name        string `json:"name" binding:"required,max=200"`
	Path        string `json:"path" binding:"required,max=4096"`
	Description string `json:"description" binding:"max=2000"`
}
type PageReq struct {
	Keyword  string `form:"keyword" binding:"max=200"`
	Page     int    `form:"page,default=1" binding:"min=1,max=1000000"`
	PageSize int    `form:"page_size,default=20" binding:"min=1,max=100"`
}
type Resp struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
type PageResp struct {
	Items    []Resp `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}
type DirectoryReq struct {
	Path string `form:"path" binding:"max=4096"`
}
type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type DirectoryResp struct {
	Home   string      `json:"home"`
	Path   string      `json:"path"`
	Parent string      `json:"parent"`
	Items  []Directory `json:"items"`
}
