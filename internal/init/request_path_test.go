package initialize

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/lyonmu/kaguya/internal/consts"
	_ "github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
)

// 旧库只有一个 api.json 目录地址，迁移后提供商地址沿用旧值，模型地址换成 models.json。
func TestCatalogSyncURLMigration(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:init-catalog-url?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	// 模拟旧库：没有提供商地址，模型地址指向 api.json，并带上自定义镜像场景。
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetProviderSyncURL("").SetModelSyncURL(consts.DefaultProviderCatalogURL).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ProviderSyncURL != consts.DefaultProviderCatalogURL || row.ModelSyncURL != consts.DefaultModelCatalogURL {
		t.Fatalf("migrated URLs=%q / %q", row.ProviderSyncURL, row.ModelSyncURL)
	}
	// 再跑一次不能继续改写。
	before := row.UpdatedAt
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	after, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before) || after.ProviderSyncURL != row.ProviderSyncURL || after.ModelSyncURL != row.ModelSyncURL {
		t.Fatalf("second run changed config: %+v", after)
	}
}

// 升级前模型没有 request_path，回填旧运行时的协议后缀保持请求地址不变；已配置的路径不动。
func TestBackfillModelRequestPaths(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:init-model-path?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaProviderInfo.Create().SetID("p").SetProviderName("legacy").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	models := []struct {
		id       string
		protocol consts.ProviderProtocol
		path     string
		want     string
	}{
		{id: "chat", protocol: consts.ProtocolOpenAIChat, want: "/chat/completions"},
		{id: "responses", protocol: consts.ProtocolOpenAIResponses, want: "/responses"},
		{id: "anthropic", protocol: consts.ProtocolAnthropic, want: "/messages"},
		{id: "configured", protocol: consts.ProtocolAnthropic, path: "/anthropic/v1/messages", want: "/anthropic/v1/messages"},
	}
	for _, item := range models {
		builder := client.KaguyaModelsInfo.Create().
			SetID(item.id).SetProviderID("p").SetModelName(item.id).SetModelID(item.id).SetAPIProtocol(item.protocol)
		if item.path != "" {
			builder.SetRequestPath(item.path)
		}
		if _, err := builder.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	rows, err := client.KaguyaModelsInfo.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(rows))
	for _, row := range rows {
		got[row.ID] = row.RequestPath
	}
	for _, item := range models {
		if got[item.id] != item.want {
			t.Errorf("model %s request path=%q, want %q", item.id, got[item.id], item.want)
		}
	}
	// 幂等：回填后再次启动不会改写任何路径。
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	again, err := client.KaguyaModelsInfo.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range again {
		if row.RequestPath != got[row.ID] {
			t.Errorf("second run changed model %s path: %q -> %q", row.ID, got[row.ID], row.RequestPath)
		}
	}
}
