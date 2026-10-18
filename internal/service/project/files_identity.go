package project

// fileSearchRootID仅进入不可逆root摘要，不返回主机身份或路径明文。
type fileSearchRootID struct {
	Volume uint64 `json:"volume"`
	Object uint64 `json:"object"`
}
