package system

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPCRUDLifecycleAndRestore(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	manager := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()
	defer func() { agentmcp.Default.Close(); agentmcp.Default = manager }()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	svc := &SystemSvc{}
	req := &dtosystem.SystemMCPSaveReq{Name: "MCP", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 2, Headers: map[string]string{"Authorization": "secret"}}
	created, err := svc.MCPCreate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Enabled || created.Status.State != "stopped" {
		t.Fatal("new config should be disabled")
	}
	if _, err := svc.MCPCreate(ctx, req); !errors.Is(err, ErrMCPDuplicate) {
		t.Fatalf("duplicate err=%v", err)
	}
	page, err := svc.MCPPage(ctx, &dtosystem.SystemMCPPageReq{Page: 1, PageSize: 10, Name: "MC"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Headers != nil {
		t.Fatalf("invalid list or leaked headers: %+v", page)
	}
	enabled, err := svc.MCPSetEnabled(ctx, created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Status.State != "running" || len(agentmcp.Default.Tools()) != 1 {
		t.Fatalf("enabled=%+v", enabled)
	}
	oldTools := agentmcp.Default.Tools()
	req.Name = "Updated"
	if _, err := svc.MCPUpdate(ctx, created.ID, req); err != nil {
		t.Fatal(err)
	}
	if len(oldTools) != 1 || len(agentmcp.Default.Tools()) != 1 {
		t.Fatal("wrong tools after replacement")
	}
	badRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret should not leak", http.StatusUnauthorized)
	}))
	defer badRemote.Close()
	invalid := *req
	invalid.URL = badRemote.URL
	if _, err := svc.MCPUpdate(ctx, created.ID, &invalid); !errors.Is(err, ErrMCPConnect) {
		t.Fatalf("bad update err=%v", err)
	}
	detail, err := svc.MCPDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.URL != remote.URL || detail.Status.State != "running" || detail.Headers["Authorization"] != "secret" {
		t.Fatalf("failed edit changed config: %+v", detail)
	}
	agentmcp.Default.Close()
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	if agentmcp.Default.Status(created.ID).State != "running" {
		t.Fatal("enabled config not restored")
	}
	stopped, err := svc.MCPSetEnabled(ctx, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Enabled || len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("stop failed")
	}
	if _, err := svc.MCPUpdate(ctx, created.ID, &invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); !errors.Is(err, ErrMCPConnect) {
		t.Fatal("broken config enabled")
	}
	detail, err = svc.MCPDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Enabled || detail.Status.State != "error" {
		t.Fatal("failed enable persisted true or hid failure")
	}
	if _, err := svc.MCPUpdate(ctx, created.ID, req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.MCPDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("delete kept tools")
	}
	if _, err := svc.MCPDetail(ctx, created.ID); !errors.Is(err, ErrMCPNotFound) {
		t.Fatalf("deleted detail err=%v", err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); !errors.Is(err, ErrMCPNotFound) {
		t.Fatal("deleted server enabled")
	}
	row, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.IDEQ(created.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.DeletedAt == nil || row.Enabled || len(row.Headers) != 0 {
		t.Fatal("deleted row was not cleaned")
	}
	if _, err := svc.MCPCreate(ctx, req); err != nil {
		t.Fatalf("recreate deleted name: %v", err)
	}
}
