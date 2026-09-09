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
	p1, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "first", APIProtocol: consts.ProtocolOpenAIChat})
	if err != nil {
		t.Fatal(err)
	}
	if p1.ProviderType != consts.ProviderTypeNormal {
		t.Fatal("missing normal default")
	}
	p2, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "second", APIProtocol: consts.ProtocolOpenAIChat, ProviderType: consts.ProviderTypeOpenCodeGo})
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
	config := dtosystem.SystemInfoSaveReq{UserAgent: "test", DefaultModelID: m1.ID, TaskModelID: m1.ID}
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
