package system

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestSystemInfoDoesNotImportLegacySelections(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	client, svc := db.EntClient, &SystemSvc{}
	p, err := client.KaguyaProviderInfo.Create().SetProviderName("legacy").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("old").SetModelID("old").SetIsDefault(consts.IsTrue).SetIsTask(consts.IsTrue).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := svc.Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" || info.UserAgent != consts.DefaultUserAgent || info.GlobalSystemPrompt != consts.GlobalSystemPrompt {
		t.Fatalf("query imported legacy selections: %+v %v", info, err)
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: "agent/2", SystemPrompt: "custom"}); err != nil {
		t.Fatal(err)
	}
	info, err = (&SystemSvc{}).Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" || info.UserAgent != "agent/2" || info.SystemPrompt != "custom" {
		t.Fatalf("cleared config regressed to legacy flags: %+v %v", info, err)
	}
	if count, err := client.KaguyaSystemInfo.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestSystemInfoReadDoesNotInitializeMissingData(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	if err := db.EntClient.KaguyaSystemInfo.DeleteOneID(consts.SystemInfoID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SystemSvc{}).Info(ctx); err == nil {
		t.Fatal("missing config should fail without writing")
	}
	if count, err := db.EntClient.KaguyaSystemInfo.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("query initialized config: count=%d err=%v", count, err)
	}
}

func TestSystemInfoValidationAndNoCache(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	info, err := svc.Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" {
		t.Fatalf("initial=%+v %v", info, err)
	}
	for _, agent := range []string{"", " ", "test\r\nInjected: true", "tab\t", "中文", strings.Repeat("a", 513)} {
		if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: agent}); !errors.Is(err, ErrInvalidSystemInfo) {
			t.Fatalf("invalid UA %q: %v", agent, err)
		}
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: "ok", SystemPrompt: strings.Repeat("a", 20001)}); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("oversized prompt: %v", err)
	}
	for _, req := range []dtosystem.SystemInfoSaveReq{{UserAgent: "ok", DefaultModelID: "missing"}, {UserAgent: "ok", TaskModelID: "missing"}} {
		if _, err := svc.InfoUpdate(ctx, &req); !errors.Is(err, ErrModelNotFound) {
			t.Fatalf("invalid model: %v", err)
		}
	}
	info, err = svc.Info(ctx)
	if err != nil || info.UserAgent != consts.DefaultUserAgent {
		t.Fatal("failed update modified config")
	}
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetUserAgent("direct-update").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = svc.Info(ctx)
	if err != nil || info.UserAgent != "direct-update" {
		t.Fatal("config was cached")
	}
}

func TestSystemInfoRejectsDeletedModelsAndProviders(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	client, svc := db.EntClient, &SystemSvc{}
	p, err := client.KaguyaProviderInfo.Create().SetProviderName("deleted").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("model").SetModelID("model").SetIsDefault(consts.IsTrue).SetIsTask(consts.IsTrue).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaProviderInfo.UpdateOne(p).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := svc.Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" {
		t.Fatalf("imported deleted provider: %+v %v", info, err)
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: "ok", DefaultModelID: m.ID}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("deleted provider selected: %v", err)
	}
	if err := client.KaguyaProviderInfo.UpdateOne(p).ClearDeletedAt().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ModelDelete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{UserAgent: "ok", TaskModelID: m.ID}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("deleted model selected: %v", err)
	}
}

func TestChatSystemPrompt(t *testing.T) {
	for _, blank := range []string{"", " \n\t"} {
		if got := ChatSystemPrompt(blank); got != consts.GlobalSystemPrompt {
			t.Fatalf("prompt=%q", got)
		}
	}
	if got := ChatSystemPrompt("请使用中文"); got != consts.GlobalSystemPrompt+"\n\n请使用中文" {
		t.Fatalf("prompt=%q", got)
	}
}
