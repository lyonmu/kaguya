package system

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

type testIDGenerator struct{ value atomic.Int64 }

func (g *testIDGenerator) GenID() (int64, error) {
	return g.value.Add(1), nil
}

func setupSystemServiceTest(t *testing.T) context.Context {
	t.Helper()
	client, err := ent.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", t.Name()))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); err != nil {
		client.Close()
		t.Fatalf("create schema: %v", err)
	}

	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient = client
	global.Id = &testIDGenerator{}
	global.Logger = zap.NewNop()
	t.Cleanup(func() {
		_ = client.Close()
		db.EntClient = oldClient
		global.Id = oldID
		global.Logger = oldLogger
	})
	if err := initialize.Run(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	return context.Background()
}

func modelSaveReq(providerID, name, modelID string) *dtosystem.SystemModelSaveReq {
	return &dtosystem.SystemModelSaveReq{
		ProviderID: providerID, ModelName: name, ModelID: modelID,
		ReasoningEnabled: consts.IsTrue, ReasoningEffort: consts.ReasoningEffortMedium,
		TokenContextWindow: 128000, TokenMaxOutputTokens: 8192,
		CapabilityToolUse: consts.IsTrue, CapabilityVision: consts.IsTrue,
		CapabilityStructuredOutput: consts.IsTrue,
	}
}

func TestProviderAndModelCRUD(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}

	provider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
		ProviderName: "OpenAI", APIProtocol: consts.ProtocolOpenAIChat,
		APIKey: "secret", BaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provider, err = svc.ProviderUpdate(ctx, provider.ID, &dtosystem.SystemProviderSaveReq{
		ProviderName: "OpenAI Updated", APIProtocol: consts.ProtocolOpenAIResponses,
		APIKey: "new-secret", BaseURL: "https://api.openai.com/v1",
	})
	if err != nil || provider.ProviderName != "OpenAI Updated" {
		t.Fatalf("update provider: resp=%+v err=%v", provider, err)
	}

	first, err := svc.ModelCreate(ctx, modelSaveReq(provider.ID, "GPT First", "gpt-first"))
	if err != nil {
		t.Fatalf("create first model: %v", err)
	}
	second, err := svc.ModelCreate(ctx, modelSaveReq(provider.ID, "GPT Second", "gpt-second"))
	if err != nil {
		t.Fatalf("create second model: %v", err)
	}
	firstDefault, err := db.EntClient.KaguyaModelsInfo.Query().Where(kaguyamodelsinfo.IDEQ(first.ID)).Only(ctx)
	if err != nil || firstDefault.IsDefault != consts.IsFalse {
		t.Fatalf("creating a model must not set a default: model=%+v err=%v", firstDefault, err)
	}

	detail, err := svc.ProviderDetail(ctx, provider.ID)
	if err != nil || len(detail.Models) != 2 {
		t.Fatalf("provider relation was not loaded: detail=%+v err=%v", detail, err)
	}
	labels, err := svc.ModelLabels(ctx, &dtosystem.SystemModelLabelReq{ProviderID: provider.ID})
	if err != nil || len(labels) != 2 {
		t.Fatalf("query model labels: labels=%+v err=%v", labels, err)
	}

	updatedReq := modelSaveReq(provider.ID, "GPT Second Updated", "gpt-second-updated")
	updated, err := svc.ModelUpdate(ctx, second.ID, updatedReq)
	if err != nil || updated.ModelName != updatedReq.ModelName {
		t.Fatalf("update model: resp=%+v err=%v", updated, err)
	}
	if err = svc.ModelDelete(ctx, second.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	if _, err = svc.ModelDetail(ctx, second.ID); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("deleted model should not be queryable: %v", err)
	}

	if err = svc.ProviderDelete(ctx, provider.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if _, err = svc.ProviderDetail(ctx, provider.ID); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("deleted provider should not be queryable: %v", err)
	}
	modelPage, err := svc.ModelPage(ctx, &dtosystem.SystemModelPageReq{Page: 1, PageSize: 10})
	if err != nil || modelPage.Total != 0 {
		t.Fatalf("provider models should be cascade soft-deleted: page=%+v err=%v", modelPage, err)
	}
}

func TestDefaultModelIsUniqueGlobally(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}

	firstProvider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
		ProviderName: "OpenAI", APIProtocol: consts.ProtocolOpenAIChat,
	})
	if err != nil {
		t.Fatalf("create first provider: %v", err)
	}
	secondProvider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
		ProviderName: "Anthropic", APIProtocol: consts.ProtocolAnthropic,
	})
	if err != nil {
		t.Fatalf("create second provider: %v", err)
	}

	firstModel, err := svc.ModelCreate(ctx, modelSaveReq(firstProvider.ID, "GPT", "gpt"))
	if err != nil {
		t.Fatalf("create first default model: %v", err)
	}
	secondModel, err := svc.ModelCreate(ctx, modelSaveReq(secondProvider.ID, "Claude", "claude"))
	if err != nil {
		t.Fatalf("create second default model: %v", err)
	}
	for _, id := range []string{firstModel.ID, secondModel.ID, firstModel.ID} {
		if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: "test", DefaultModelID: id}); err != nil {
			t.Fatal(err)
		}
		info, err := svc.Info(ctx)
		if err != nil || info.DefaultModelID != id {
			t.Fatalf("default selection: %+v %v", info, err)
		}
	}
	if count, err := db.EntClient.KaguyaSystemInfo.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("system config count=%d err=%v", count, err)
	}
	if _, err := svc.ModelUpdate(ctx, firstModel.ID, modelSaveReq(firstProvider.ID, "GPT Updated", "gpt")); err != nil {
		t.Fatal(err)
	}
	info, err := svc.Info(ctx)
	if err != nil || info.DefaultModelID != firstModel.ID {
		t.Fatalf("model edit changed system config: %+v %v", info, err)
	}
}

func TestProviderAndModelDuplicateValidation(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	req := &dtosystem.SystemProviderSaveReq{ProviderName: "Anthropic", APIProtocol: consts.ProtocolAnthropic}
	provider, err := svc.ProviderCreate(ctx, req)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err = svc.ProviderCreate(ctx, req); !errors.Is(err, ErrProviderDuplicate) {
		t.Fatalf("expected duplicate provider error, got %v", err)
	}
	modelReq := modelSaveReq(provider.ID, "Claude", "claude")
	if _, err = svc.ModelCreate(ctx, modelReq); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err = svc.ModelCreate(ctx, modelReq); !errors.Is(err, ErrModelDuplicate) {
		t.Fatalf("expected duplicate model error, got %v", err)
	}
}
