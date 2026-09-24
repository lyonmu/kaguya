package system

import (
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestTaskModelUniqueAndIndependent(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	p1, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if p1.ProviderType != consts.ProviderTypeNormal {
		t.Fatal("missing normal default")
	}
	p2, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "second", ProviderType: consts.ProviderTypeOpenCodeGo})
	if err != nil {
		t.Fatal(err)
	}
	if p2.ProviderType != consts.ProviderTypeOpenCodeGo {
		t.Fatal("provider type not saved")
	}
	r1, r2 := modelSaveReq(p1.ID, "first", "same-api-id"), modelSaveReq(p2.ID, "second", "same-api-id")
	m1, err := svc.ModelCreate(ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := svc.ModelCreate(ctx, r2)
	if err != nil {
		t.Fatal(err)
	}
	config := dtosystem.SystemInfoSaveReq{DefaultModelID: m1.ID, TaskModelID: m1.ID}
	check := func(defaultID, taskID string) {
		t.Helper()
		info, err := svc.Info(ctx)
		if err != nil || info.DefaultModelID != defaultID || info.TaskModelID != taskID {
			t.Fatalf("system config=%+v err=%v", info, err)
		}
		if n, err := db.EntClient.KaguyaSystemInfo.Query().Count(ctx); err != nil || n != 1 {
			t.Fatalf("singleton count=%d err=%v", n, err)
		}
	}
	if _, err := svc.InfoUpdate(ctx, &config); err != nil {
		t.Fatal(err)
	}
	check(m1.ID, m1.ID)
	config.TaskModelID = m2.ID
	if _, err := svc.InfoUpdate(ctx, &config); err != nil {
		t.Fatal(err)
	}
	check(m1.ID, m2.ID)
	if _, err := svc.ModelUpdate(ctx, m1.ID, r1); err != nil {
		t.Fatal(err)
	}
	check(m1.ID, m2.ID)
	if _, err := svc.ModelCreate(ctx, r2); err == nil {
		t.Fatal("expected duplicate model failure")
	}
	check(m1.ID, m2.ID)
	invalid := config
	invalid.TaskModelID = "missing"
	if _, err := svc.InfoUpdate(ctx, &invalid); err != ErrModelNotFound {
		t.Fatalf("invalid selection: %v", err)
	}
	check(m1.ID, m2.ID)
	config.TaskModelID = ""
	if _, err := svc.InfoUpdate(ctx, &config); err != nil {
		t.Fatal(err)
	}
	check(m1.ID, "")
	config.TaskModelID = m2.ID
	if _, err := svc.InfoUpdate(ctx, &config); err != nil {
		t.Fatal(err)
	}
	if err := svc.ModelDelete(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	check("", m2.ID)
	if err := svc.ProviderDelete(ctx, p2.ID); err != nil {
		t.Fatal(err)
	}
	check("", "")
}

// ResolveTaskModel 返回不可变调用快照；未配置、模型删除与密钥不可用分别映射为
// 明确错误，绝不悄悄回退到聊天模型。
func TestResolveTaskModelSnapshot(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	if _, err := ResolveTaskModel(ctx, db.EntClient, "conv-1"); err != ErrTaskModelNotConfigured {
		t.Fatalf("unconfigured: %v", err)
	}
	provider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
		ProviderName: "task-provider", APIKey: "api-key", BaseURL: "https://example.invalid/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := svc.ModelCreate(ctx, modelSaveReq(provider.ID, "task-model", "upstream-model"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{TaskModelID: model.ID}); err != nil {
		t.Fatal(err)
	}
	task, err := ResolveTaskModel(ctx, db.EntClient, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ModelRecordID != model.ID || task.ProviderID != provider.ID || task.UpstreamModelID != "upstream-model" {
		t.Fatalf("snapshot identity=%+v", task)
	}
	if task.Config.APIKey != "api-key" || task.Config.ConversationID != "conv-1" || task.Config.RequestPath != "/v1/chat/completions" {
		t.Fatalf("snapshot config=%+v", task.Config)
	}
	if task.TokenContextWindow != 128000 || task.TokenMaxOutputTokens != 8192 {
		t.Fatalf("snapshot limits: window=%d output=%d", task.TokenContextWindow, task.TokenMaxOutputTokens)
	}
	// 密钥不可用：不能返回配置，也不能回退到聊天模型。
	if err := db.EntClient.KaguyaProviderInfo.UpdateOneID(provider.ID).SetAPIKey("enc:v2:broken").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTaskModel(ctx, db.EntClient, "conv-1"); err != ErrProviderSecret {
		t.Fatalf("broken secret: %v", err)
	}
	// 模型删除后回到未配置错误。
	if err := db.EntClient.KaguyaProviderInfo.UpdateOneID(provider.ID).SetAPIKey("api-key").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ModelDelete(ctx, model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTaskModel(ctx, db.EntClient, "conv-1"); err != ErrTaskModelNotConfigured {
		t.Fatalf("deleted model: %v", err)
	}
}
