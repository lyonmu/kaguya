package system

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/global"
)

func TestFront(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalPrefix := global.Cfg.RouterPrefix
	global.Cfg.RouterPrefix = "/kaguya/api"
	t.Cleanup(func() {
		global.Cfg.RouterPrefix = originalPrefix
	})

	api := SystemApiV1Group{}
	router := gin.New()
	router.GET("/", api.Front)
	router.HEAD("/", api.Front)
	router.NoRoute(api.Front)

	index := performFrontRequest(t, router, http.MethodGet, "/", "text/html")
	if index.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d", index.Code)
	}
	if !strings.Contains(index.Body.String(), `<div id="root"></div>`) {
		t.Fatal("index response does not contain the frontend root element")
	}
	if cacheControl := index.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("unexpected index cache control: %q", cacheControl)
	}

	assetPath := findAssetPath(t, index.Body.String())
	asset := performFrontRequest(t, router, http.MethodGet, assetPath, "*/*")
	if asset.Code != http.StatusOK {
		t.Fatalf("unexpected asset status: %d", asset.Code)
	}
	if cacheControl := asset.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "immutable") {
		t.Fatalf("asset is missing immutable cache control: %q", cacheControl)
	}

	spa := performFrontRequest(t, router, http.MethodGet, "/system/access-logs", "text/html")
	if spa.Code != http.StatusOK {
		t.Fatalf("unexpected SPA fallback status: %d", spa.Code)
	}
	if !strings.Contains(spa.Body.String(), `<div id="root"></div>`) {
		t.Fatal("SPA fallback did not return index.html")
	}

	apiNotFound := performFrontRequest(t, router, http.MethodGet, "/kaguya/api/not-found", "text/html")
	if apiNotFound.Code != http.StatusNotFound {
		t.Fatalf("unknown API path should return 404, got %d", apiNotFound.Code)
	}

	assetNotFound := performFrontRequest(t, router, http.MethodGet, "/assets/not-found.js", "*/*")
	if assetNotFound.Code != http.StatusNotFound {
		t.Fatalf("unknown asset should return 404, got %d", assetNotFound.Code)
	}
}

func performFrontRequest(t *testing.T, handler http.Handler, method, target, accept string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("Accept", accept)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func findAssetPath(t *testing.T, index string) string {
	t.Helper()

	match := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindStringSubmatch(index)
	if len(match) != 2 {
		t.Fatal("could not find a built frontend asset in index.html")
	}
	return match[1]
}
