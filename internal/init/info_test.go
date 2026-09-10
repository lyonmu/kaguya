package initialize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/lyonmu/kaguya/internal/consts"
	_ "github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
)

func TestInfoInitializationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:init-info?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	// 即使有旧标记，初始化也不选择模型。
	if err := client.KaguyaProviderInfo.Create().SetID("p").SetProviderName("legacy").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaModelsInfo.Create().SetID("m").SetProviderID("p").SetModelName("legacy").SetModelID("legacy").SetIsDefault(consts.IsTrue).SetIsTask(consts.IsTrue).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if row.UserAgent != consts.DefaultUserAgent || row.SystemPrompt != "" || row.DefaultModelID != "" || row.TaskModelID != "" {
		t.Fatalf("defaults=%+v", row)
	}
	before, err := client.KaguyaSystemInfo.UpdateOne(row).SetUserAgent("custom/1").SetSystemPrompt("custom prompt").SetDefaultModelID("saved-default").SetTaskModelID("saved-task").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Run(ctx, client); err != nil {
			t.Fatal(err)
		}
	}
	after, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UserAgent != before.UserAgent || after.SystemPrompt != before.SystemPrompt || after.DefaultModelID != before.DefaultModelID || after.TaskModelID != before.TaskModelID || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("initialization overwrote config: %+v", after)
	}
	if count, err := client.KaguyaSystemInfo.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

var errCaptured = errors.New("captured SQL execution")

// 捕获真实 ent 生成的 SQL，不依赖 PostgreSQL/MySQL 实例或额外 mock 依赖。
type captureDriver struct{ kind, statement string }

func (d *captureDriver) Dialect() string { return d.kind }
func (d *captureDriver) Close() error    { return nil }
func (d *captureDriver) Tx(context.Context) (dialect.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (d *captureDriver) Exec(_ context.Context, query string, _ any, _ any) error {
	d.statement = query
	return errCaptured
}
func (d *captureDriver) Query(_ context.Context, query string, _ any, _ any) error {
	d.statement = query
	return errCaptured
}

func TestInfoInitializationSQLDialects(t *testing.T) {
	for _, kind := range []string{dialect.Postgres, dialect.SQLite, dialect.MySQL} {
		t.Run(kind, func(t *testing.T) {
			driver := &captureDriver{kind: kind}
			client := ent.NewClient(ent.Driver(driver))
			defer client.Close()
			if err := Run(context.Background(), client); !errors.Is(err, errCaptured) {
				t.Fatalf("initialization must propagate SQL failure: %v", err)
			}
			want := "ON CONFLICT (`id`) DO UPDATE SET"
			if kind == dialect.Postgres {
				want = `ON CONFLICT ("id") DO UPDATE SET`
			}
			if kind == dialect.MySQL {
				want = "ON DUPLICATE KEY UPDATE"
			}
			if !strings.Contains(driver.statement, want) {
				t.Fatalf("missing explicit conflict target for %s: %s", kind, driver.statement)
			}
		})
	}
}
