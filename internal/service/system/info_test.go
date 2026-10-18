package system

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestSystemInfoDoesNotSelectModelsImplicitly(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	client, svc := db.EntClient, &SystemSvc{}
	p, err := client.KaguyaProviderInfo.Create().SetProviderName("legacy").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("old").SetModelID("old").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := svc.Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" || info.GlobalSystemPrompt == nil || *info.GlobalSystemPrompt != consts.GlobalSystemPrompt || info.ModelSyncURL != consts.DefaultModelCatalogURL {
		t.Fatalf("query selected a model implicitly: %+v %v", info, err)
	}
	base := "new base"
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{GlobalSystemPrompt: &base, SystemPrompt: "custom"}); err != nil {
		t.Fatal(err)
	}
	info, err = (&SystemSvc{}).Info(ctx)
	if err != nil || info.DefaultModelID != "" || info.TaskModelID != "" || info.GlobalSystemPrompt == nil || *info.GlobalSystemPrompt != base || info.SystemPrompt != "custom" {
		t.Fatalf("cleared config gained implicit selections: %+v %v", info, err)
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
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{SystemPrompt: strings.Repeat("a", 20001)}); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("oversized prompt: %v", err)
	}
	oversizedBase := strings.Repeat("a", 20001)
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{GlobalSystemPrompt: &oversizedBase}); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("oversized base prompt: %v", err)
	}
	for _, syncURL := range []string{"models.dev/models.json", "ftp://models.dev/models.json", "https://user:secret@models.dev/models.json", "https://models.dev/models.json#fragment", " https://models.dev/models.json"} {
		if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{ModelSyncURL: syncURL}); !errors.Is(err, ErrInvalidSystemInfo) {
			t.Fatalf("accepted invalid sync URL %q: %v", syncURL, err)
		}
		if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{ProviderSyncURL: syncURL}); !errors.Is(err, ErrInvalidSystemInfo) {
			t.Fatalf("accepted invalid provider sync URL %q: %v", syncURL, err)
		}
	}
	// 两个目录地址相互独立，保存后都按原值返回。
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{ModelSyncURL: "https://models.dev/models.json", ProviderSyncURL: "https://models.dev/api.json"}); err != nil {
		t.Fatal(err)
	}
	info, err = svc.Info(ctx)
	if err != nil || info.ModelSyncURL != "https://models.dev/models.json" || info.ProviderSyncURL != "https://models.dev/api.json" {
		t.Fatalf("sync URLs=%q / %q err=%v", info.ModelSyncURL, info.ProviderSyncURL, err)
	}
	for _, req := range []dtosystem.SystemInfoSaveReq{{DefaultModelID: "missing"}, {TaskModelID: "missing"}} {
		if _, err := svc.InfoUpdate(ctx, &req); !errors.Is(err, ErrModelNotFound) {
			t.Fatalf("invalid model: %v", err)
		}
	}
	info, err = svc.Info(ctx)
	if err != nil || info.GlobalSystemPrompt == nil || *info.GlobalSystemPrompt != consts.GlobalSystemPrompt {
		t.Fatal("failed update modified config")
	}
	direct := "direct update"
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalSystemPrompt(direct).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = svc.Info(ctx)
	if err != nil || info.GlobalSystemPrompt == nil || *info.GlobalSystemPrompt != direct {
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
	m, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("model").SetModelID("model").Save(ctx)
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
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{DefaultModelID: m.ID}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("deleted provider selected: %v", err)
	}
	if err := client.KaguyaProviderInfo.UpdateOne(p).ClearDeletedAt().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ModelDelete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{TaskModelID: m.ID}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("deleted model selected: %v", err)
	}
}

func TestChatSystemPrompt(t *testing.T) {
	for _, blank := range []string{"", " \n\t"} {
		if got := ChatSystemPrompt(consts.GlobalSystemPrompt, blank); got != strings.TrimSpace(consts.GlobalSystemPrompt) {
			t.Fatalf("prompt=%q", got)
		}
	}
	if got := ChatSystemPrompt(consts.GlobalSystemPrompt, "请使用中文"); got != strings.TrimSpace(consts.GlobalSystemPrompt)+"\n\n请使用中文" {
		t.Fatalf("prompt=%q", got)
	}
	if got := ChatSystemPrompt("", "custom"); got != "custom" {
		t.Fatalf("empty base prompt=%q", got)
	}
}

func TestAgentSettingsPersistenceAndValidation(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	info, err := svc.Info(ctx)
	if err != nil || *info.AgentMaxSteps != 0 || *info.CommandTimeoutSeconds != 120 || *info.ChatMaxRetries != 5 || len(info.GlobalAgentsPaths) != 2 {
		t.Fatalf("defaults=%+v %v", info, err)
	}
	steps, timeout, retries := 100, 300, 7
	percent := 75
	if *info.ContextCompactionPercent != 90 {
		t.Fatal("wrong default compaction percent")
	}
	interval := 12
	req := &dtosystem.SystemInfoSaveReq{AgentMaxSteps: &steps, CommandTimeoutSeconds: &timeout, ChatMaxRetries: &retries, GlobalAgentsPaths: []string{}, ContextCompactionPercent: &percent, ModelSyncEnabled: true, ModelSyncIntervalHours: &interval}
	info, err = svc.InfoUpdate(ctx, req)
	if err != nil || *info.AgentMaxSteps != 100 || *info.CommandTimeoutSeconds != 300 || *info.ChatMaxRetries != 7 || info.GlobalAgentsPaths == nil || len(info.GlobalAgentsPaths) != 0 {
		t.Fatalf("saved=%+v %v", info, err)
	}
	info, err = svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{})
	if err != nil || *info.AgentMaxSteps != 100 || len(info.GlobalAgentsPaths) != 0 || *info.ContextCompactionPercent != 75 {
		t.Fatalf("old client reset settings: %+v %v", info, err)
	}
	steps = -1
	for _, invalid := range []int{0, 9, 96, 100} {
		if _, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{ContextCompactionPercent: &invalid}); !errors.Is(err, ErrInvalidSystemInfo) {
			t.Fatalf("accepted percent %d", invalid)
		}
	}
	if _, err = svc.InfoUpdate(ctx, req); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("invalid steps: %v", err)
	}
	steps = 1
	retries = 21
	if _, err = svc.InfoUpdate(ctx, req); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("invalid retries: %v", err)
	}
	retries = 7
	req.GlobalAgentsPaths = []string{"relative/AGENTS.md"}
	if _, err = svc.InfoUpdate(ctx, req); !errors.Is(err, ErrInvalidSystemInfo) {
		t.Fatalf("relative path: %v", err)
	}
}

func TestInstructionPathsCommitAndNotificationUseSameOrder(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	oldReload := ReloadGlobalInstructions
	defer func() { ReloadGlobalInstructions = oldReload }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var notifications []string
	ReloadGlobalInstructions = func(paths []string) error {
		if len(paths) != 1 {
			return errors.New("unexpected fixture paths")
		}
		notifications = append(notifications, paths[0])
		if paths[0] == "/first.md" {
			close(entered)
			<-release
		}
		return nil
	}
	svc := &SystemSvc{}
	first := make(chan error, 1)
	go func() { first <- svc.UpdateAgentsPaths(ctx, []string{"/first.md"}) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first commit did not reach notification")
	}
	second := make(chan error, 1)
	go func() {
		_, err := svc.InfoUpdate(ctx, &dtosystem.SystemInfoSaveReq{GlobalAgentsPaths: []string{"/second.md"}})
		second <- err
	}()
	// 首次通知尚未结束时，另一个入口不能提交更新或发出较新的通知。
	select {
	case err := <-second:
		t.Fatalf("second update bypassed ordering: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil || len(row.GlobalAgentsPaths) != 1 || row.GlobalAgentsPaths[0] != "/first.md" {
		t.Fatalf("commit overtook first notification: %+v %v", row, err)
	}
	unblock()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	row, err = db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil || len(row.GlobalAgentsPaths) != 1 || row.GlobalAgentsPaths[0] != "/second.md" {
		t.Fatalf("latest config=%+v %v", row, err)
	}
	if !reflect.DeepEqual(notifications, []string{"/first.md", "/second.md"}) {
		t.Fatalf("notifications=%v", notifications)
	}
	// 提交失败不能通知未保存的配置。
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.UpdateAgentsPaths(cancelCtx, []string{"/unsaved.md"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	if len(notifications) != 2 {
		t.Fatal("failed save notified manager")
	}
}
