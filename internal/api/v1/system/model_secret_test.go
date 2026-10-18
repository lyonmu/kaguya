package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"github.com/lyonmu/kaguya/internal/secret"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestModelTestProviderBodyNeverEntersLogs(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := filepath.Join(home, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&config.DatabaseConfig{Path: filepath.Join(home, "data.db"), KeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	core, logs := observer.New(zap.DebugLevel)
	oldClient, oldLogger, oldID := db.EntClient, global.Logger, global.Id
	db.EntClient, global.Logger, global.Id = client, zap.New(core), &cacheTestID{}
	defer func() { db.EntClient, global.Logger, global.Id = oldClient, oldLogger, oldID }()
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	defer secret.Reset()
	const marker = "fake-model-test-key-body-boundary"
	var calls atomic.Int64
	providerHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+marker {
			t.Error("model test changed the original fake provider key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error"}}`, marker)
	}))
	defer providerHost.Close()
	provider, err := (&servicesystem.SystemSvc{}).ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "boundary", APIKey: marker, BaseURL: providerHost.URL})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"provider_id": provider.ID, "model_name": "boundary", "model_id": "boundary", "api_protocol": "openai-chat", "request_path": "/v1/chat/completions", "reasoning_enabled": 2, "reasoning_effort": "low", "capability_tool_use": 2, "capability_vision": 2, "capability_structured_output": 2})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/test", (&SystemApiV1Group{}).SystemModelTest)
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(input)))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != dtocode.ModelTestFailure.Code || calls.Load() != 1 || strings.Contains(w.Body.String(), marker) {
		t.Fatal("model test changed fixed error mapping or leaked provider body", err)
	}
	for _, entry := range logs.All() {
		serialized, err := json.Marshal(entry.ContextMap())
		if err != nil || strings.Contains(entry.Message+string(serialized), marker) {
			t.Fatal("model test logged original fake provider key/body", err)
		}
	}
	if logs.FilterMessage("model test failed").Len() != 1 {
		t.Fatal("model test lost the diagnostic warning")
	}
}
