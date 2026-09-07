package system

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/global"
)

//go:embed frontend
var embeddedFrontend embed.FS

var frontendFS = mustFrontendFS()

func mustFrontendFS() fs.FS {
	frontend, err := fs.Sub(embeddedFrontend, "frontend")
	if err != nil {
		panic("create embedded frontend filesystem: " + err.Error())
	}
	return frontend
}

// Front 提供嵌入二进制的前端静态资源，并为前端路由回退到 index.html。
func (b *SystemApiV1Group) Front(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}

	requestPath := c.Request.URL.Path
	if isAPIPath(requestPath) {
		c.Status(http.StatusNotFound)
		return
	}

	filePath := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if filePath == "." || filePath == "" {
		filePath = "index.html"
	}

	fileInfo, err := fs.Stat(frontendFS, filePath)
	if err != nil || fileInfo.IsDir() {
		if path.Ext(filePath) != "" || !acceptsHTML(c) {
			c.Status(http.StatusNotFound)
			return
		}
		filePath = "index.html"
	}

	if strings.HasPrefix(filePath, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-cache")
	}
	c.Header("X-Content-Type-Options", "nosniff")
	servePath := "/" + filePath
	if filePath == "index.html" {
		servePath = "/"
	}
	c.FileFromFS(servePath, http.FS(frontendFS))
}

func acceptsHTML(c *gin.Context) bool {
	return strings.Contains(c.GetHeader("Accept"), "text/html")
}

func isAPIPath(requestPath string) bool {
	trimmedPrefix := strings.Trim(global.Cfg.RouterPrefix, "/")
	if trimmedPrefix == "" {
		return false
	}

	apiPrefix := "/" + trimmedPrefix
	return requestPath == apiPrefix || strings.HasPrefix(requestPath, apiPrefix+"/")
}
