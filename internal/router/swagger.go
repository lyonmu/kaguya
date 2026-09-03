package router

import (
	swagv1 "github.com/swaggo/swag"
	swagv2 "github.com/swaggo/swag/v2"
)

// 背景：项目使用 swag v2 生成 OpenAPI 3.1 文档，docs.go 将 spec 注册在 swag/v2 的 registry 中；
// 而 gin-swagger（v1.6.1，最新版仍为 v1 系）只从 swag v1 的 registry 读取 doc（swag.ReadDoc），
// 导致 /swagger/doc.json 返回 500 Internal Server Error。
// 这里把 swag v2 注册的文档桥接到 swag v1 registry，使 gin-swagger 能正常返回 v2 生成的文档。
type swaggerV2Bridge struct{}

func (swaggerV2Bridge) ReadDoc() string {
	if doc, err := swagv2.ReadDoc(); err == nil {
		return doc
	}
	return ""
}

func init() {
	swagv1.Register(swagv1.Name, swaggerV2Bridge{})
}