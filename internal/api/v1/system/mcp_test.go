package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/lyonmu/kaguya/internal/global"
)

type mcpTestID struct{ value atomic.Int64 }

func (g *mcpTestID) GenID() (int64, error) { return g.value.Add(1), nil }

func TestMCPAPIValidationAndCRUD(t *testing.T) {
	client, err := ent.Open(dialect.SQLite, "file:mcp-api?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldManager := db.EntClient, global.Id, agentmcp.Default
	db.EntClient, global.Id, agentmcp.Default = client, &mcpTestID{}, agentmcp.NewManager()
	defer func() {
		agentmcp.Default.Close()
		db.EntClient, global.Id, agentmcp.Default = oldClient, oldID, oldManager
	}()
	api, router := &SystemApiV1Group{}, gin.New()
	router.GET("/mcp/page", api.SystemMCPPage)
	router.GET("/mcp/:id", api.SystemMCPDetail)
	router.POST("/mcp", api.SystemMCPCreate)
	router.PUT("/mcp/:id", api.SystemMCPUpdate)
	router.PUT("/mcp/:id/state", api.SystemMCPSetEnabled)
	router.DELETE("/mcp/:id", api.SystemMCPDelete)
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		writer := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(writer, req)
		var resp struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(writer.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Code != want {
			t.Fatalf("%s %s: %s", method, path, writer.Body.String())
		}
		if method == "GET" && path == "/mcp/1" && want == 100000 && writer.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secret detail may be cached")
		}
		return resp.Data
	}
	request("POST", "/mcp", `{}`, 100001)
	request("POST", "/mcp", `{"name":"invalid","transport":"stdio","command":"echo","timeout_seconds":0}`, 100001)
	request("POST", "/mcp", `{"name":"invalid","transport":"sse","url":"file:///tmp","timeout_seconds":1}`, dtocode.MCPConfigInvalid.Code)
	valid := `{"name":"test","transport":"stdio","command":"echo","args":[],"env":{"KEY":"secret"},"timeout_seconds":1}`
	created := request("POST", "/mcp", valid, 100000)
	id := fmt.Sprint(created["id"])
	request("POST", "/mcp", valid, dtocode.MCPDuplicate.Code)
	request("GET", "/mcp/page?page=0", "", 100001)
	request("GET", "/mcp/page?page_size=101", "", 100001)
	page := request("GET", "/mcp/page", "", 100000)
	if page["total"] != float64(1) {
		t.Fatal("incorrect page")
	}
	detail := request("GET", "/mcp/"+id, "", 100000)
	if detail["env"].(map[string]any)["KEY"] != "secret" {
		t.Fatal("missing detail secrets")
	}
	request("PUT", "/mcp/"+id+"/state", `{}`, 100001)
	request("PUT", "/mcp/"+id+"/state", `{"enabled":false}`, 100000)
	request("PUT", "/mcp/"+id, strings.Replace(valid, `"test"`, `"updated"`, 1), 100000)
	request("DELETE", "/mcp/"+id, "", 100000)
	request("GET", "/mcp/"+id, "", dtocode.MCPNotFound.Code)
	request("PUT", "/mcp/"+id+"/state", `{"enabled":true}`, dtocode.MCPNotFound.Code)
}
