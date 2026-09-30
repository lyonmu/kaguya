package code

import (
	"encoding/json"
	"net/http"
)

var (
	// 传输边界响应：Web 与 Desktop 同义共码，客户端按 code 分类展示，
	// 不依赖 HTTP 状态判断业务含义。HTTP 状态仍保留给网络层与网关。
	RequestForbidden = Response{Code: 100002, Message: "请求被拒绝"}
	RequestTooLarge  = Response{Code: 100003, Message: "请求内容过大"}
	ServiceStarting  = Response{Code: 100004, Message: "服务正在启动，请稍后重试"}
	ServiceStopping  = Response{Code: 100005, Message: "服务正在退出，请稍后重试"}
	RouteNotFound    = Response{Code: 100006, Message: "接口不存在"}
	InternalFailure  = Response{Code: 100007, Message: "服务内部错误"}
)

// WriteHTTP 写出统一 envelope，供不经过 Gin 的传输边界（Desktop 原生通道、
// 关停拒绝、边缘中间件）复用。它只依赖本包类型，业务包不参与其中。
func WriteHTTP(w http.ResponseWriter, status int, response Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
