package system

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

type usageStatement struct {
	query string
	args  []any
}
type usageCaptureDriver struct {
	dialect.Driver
	capture    bool
	statements []usageStatement
}

func (d *usageCaptureDriver) Query(ctx context.Context, query string, args, v any) error {
	if d.capture {
		d.statements = append(d.statements, usageStatement{query: query, args: append([]any(nil), args.([]any)...)})
	}
	return d.Driver.Query(ctx, query, args, v)
}

// P-7 diagnostics only: real SQLCipher, existing schema/indexes, synthetic UTC data.
// 800 days, 60% completed + 10% each unfinished status, 1,000 conversations (half soft-deleted),
// 16 providers and 40 model IDs. Query templates are captured from the actual TokenUsage path.
// Run -run '^$' -bench BenchmarkTokenUsageSQLCipher -benchmem -count=5 -v.
func BenchmarkTokenUsageSQLCipher(b *testing.B) {
	for _, size := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("turns-%d", size), func(b *testing.B) {
			ctx := context.Background()
			dir := b.TempDir()
			key := filepath.Join(dir, "key")
			if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
				b.Fatal(err)
			}
			cfg := config.DatabaseConfig{Path: filepath.Join(dir, "usage.db"), KeyFile: key}
			migrated, err := db.InitSQLite(&cfg)
			if err != nil {
				b.Fatal(err)
			}
			if err := migrated.Close(); err != nil {
				b.Fatal(err)
			}
			dsn, err := cfg.SQLiteDSN()
			if err != nil {
				b.Fatal(err)
			}
			raw, err := sql.Open("sqlite3", dsn)
			if err != nil {
				b.Fatal(err)
			}
			raw.SetMaxOpenConns(1)
			raw.SetMaxIdleConns(1)
			driver := &usageCaptureDriver{Driver: entsql.OpenDB(dialect.SQLite, raw)}
			client := ent.NewClient(ent.Driver(driver))
			defer client.Close()
			old := db.EntClient
			db.EntClient = client
			defer func() { db.EntClient = old }()
			today := time.Now().UTC().Truncate(24 * time.Hour)
			req := &dtosystem.TokenUsageReq{StartTime: today.AddDate(0, 0, -6).Unix(), EndTime: today.Add(24*time.Hour - time.Second).Unix()}
			activityStart, _ := usageWindow(today)
			var wantSummary, wantActivity int64
			summaryConversations := make(map[string]bool)
			for start := 0; start < 1000; start += 200 {
				rows := make([]*ent.KaguyaConversationCreate, 0, 200)
				for i := start; i < start+200; i++ {
					r := client.KaguyaConversation.Create().SetID(fmt.Sprintf("c%d", i)).SetTitle("synthetic").SetModelID("m").SetModelName("model").SetLastMessageAt(today)
					if i%2 == 0 {
						r.SetDeletedAt(today)
					}
					rows = append(rows, r)
				}
				if err := client.KaguyaConversation.CreateBulk(rows...).Exec(ctx); err != nil {
					b.Fatal(err)
				}
			}
			for start := 0; start < size; start += 200 {
				rows := make([]*ent.KaguyaChatTurnCreate, 0, 200)
				for i := start; i < min(start+200, size); i++ {
					at := today.AddDate(0, 0, -i%800).Add(123 * time.Millisecond)
					status := kaguyachatturn.StatusCompleted
					if i%10 >= 6 {
						status = []kaguyachatturn.Status{kaguyachatturn.StatusRunning, kaguyachatturn.StatusInterrupted, kaguyachatturn.StatusCanceled, kaguyachatturn.StatusFailed}[i%10-6]
					}
					conversation := fmt.Sprintf("c%d", i%1000)
					if status == kaguyachatturn.StatusCompleted {
						if at.Unix() >= req.StartTime {
							wantSummary += 120
							summaryConversations[conversation] = true
						}
						if !at.Before(activityStart) {
							wantActivity += 120
						}
					}
					rows = append(rows, client.KaguyaChatTurn.Create().SetID(fmt.Sprintf("t%d", i)).SetConversationID(conversation).SetTurnIndex(int64(i/1000+1)).SetStatus(status).
						SetUserContent("synthetic").SetProviderID(fmt.Sprintf("p%d", i%16)).SetProviderName(fmt.Sprintf("Provider %d", i%16)).SetModelID(fmt.Sprintf("m%d", i%40)).SetModelName(fmt.Sprintf("Model %d", i%40)).SetAPIProtocol("openai").
						SetStartedAt(at).SetFinishedAt(at).SetDurationMs(1).SetToolCalls(0).SetFinishReason("stop").SetInputTokens(80).SetOutputTokens(30).SetReasoningTokens(5).SetCachedTokens(10).SetTotalTokens(120).SetMessages([]fantasy.Message{}))
				}
				if err := client.KaguyaChatTurn.CreateBulk(rows...).Exec(ctx); err != nil {
					b.Fatal(err)
				}
			}
			driver.capture = true
			response, err := (&SystemSvc{}).TokenUsage(ctx, req)
			driver.capture = false
			if err != nil {
				b.Fatal(err)
			}
			var activity int64
			for _, day := range response.Days {
				activity += day.TotalTokens
			}
			if response.TotalTokens != wantSummary || response.Conversations != int64(len(summaryConversations)) || activity != wantActivity || len(response.Models) != 10 || len(response.Providers) != 10 {
				b.Fatal("UTC/completed/soft-delete/Top-10 semantics changed")
			}
			for _, group := range append(response.Models, response.Providers...) {
				if group.InputTokens+group.OutputTokens+group.CachedTokens+group.ReasoningTokens != group.TotalTokens {
					b.Fatal("composition token accounting changed")
				}
			}
			if len(driver.statements) != 5 {
				b.Fatalf("unexpected TokenUsage query count: %d", len(driver.statements))
			}
			groups := []struct {
				name       string
				statements []usageStatement
			}{
				{"selected-summary", driver.statements[:2]},
				{"year-activity", driver.statements[2:3]},
				{"all-time-composition", driver.statements[3:]},
			}
			for _, group := range groups {
				for _, statement := range group.statements {
					rows, err := raw.QueryContext(ctx, "EXPLAIN QUERY PLAN "+statement.query, statement.args...)
					if err != nil {
						b.Fatal(err)
					}
					var details []string
					for rows.Next() {
						var id, parent, unused int
						var detail string
						if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
							b.Fatal(err)
						}
						details = append(details, detail)
					}
					err = rows.Err()
					_ = rows.Close()
					if err != nil {
						b.Fatal(err)
					}
					b.Logf("%s: %s\nSQL: %s", group.name, strings.Join(details, "; "), statement.query)
				}
				b.Run(group.name, func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						for _, statement := range group.statements {
							scanUsageStatement(b, ctx, raw, statement)
						}
					}
				})
			}
			b.Run("response", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if _, err := (&SystemSvc{}).TokenUsage(ctx, req); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// The three SQL-only subbenchmarks consume every row; response measures the full DTO path.
func scanUsageStatement(b *testing.B, ctx context.Context, raw *sql.DB, statement usageStatement) {
	b.Helper()
	rows, err := raw.QueryContext(ctx, statement.query, statement.args...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		b.Fatal(err)
	}
	values := make([]any, len(columns))
	for i := range values {
		values[i] = new(any)
	}
	for rows.Next() {
		if err := rows.Scan(values...); err != nil {
			b.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}
