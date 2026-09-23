package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	entbase "entgo.io/ent"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogManualSyncSerializesDownloads(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	var active, peak atomic.Int64
	s := NewModelCatalogSyncer(&http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
		n := active.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		defer active.Add(-1)
		time.Sleep(10 * time.Millisecond)
		return nil, errors.New("private URL: persist catalog fake-secret")
	})})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if _, err := s.Sync(ctx); !errors.Is(err, ErrModelCatalogSync) || strings.Contains(err.Error(), "fake-secret") {
				t.Errorf("unsafe sync error: %v", err)
			}
		})
	}
	workers.Wait()
	if active.Load() != 0 || peak.Load() != 1 {
		t.Fatal("manual synchronization was not serialized")
	}
	if s.runtimeError() != "模型目录同步失败，请检查网络与目录配置" {
		t.Fatal("raw error text influenced classification")
	}
}

func TestCatalogRuntimeErrorOrdering(t *testing.T) {
	s := NewModelCatalogSyncer(http.DefaultClient)
	for _, set := range []func(uint64, string){s.setScheduleError, s.setSyncError} {
		old, newer := s.nextSequence(), s.nextSequence()
		set(newer, "new")
		set(old, "")
		if s.runtimeError() != "new" {
			t.Fatal("late success erased newer failure")
		}
		set(newer+1, "")
		set(old, "old")
		if s.runtimeError() != "" {
			t.Fatal("late failure resurrected after successful recovery")
		}
	}
	s.setScheduleError(100, "schedule")
	s.setSyncError(101, "sync")
	if s.runtimeError() != "sync" {
		t.Fatal("newest unresolved error not shown")
	}
	s.setSyncError(102, "")
	if s.runtimeError() != "schedule" {
		t.Fatal("successful sync hid unresolved scheduler error")
	}
	s.setScheduleError(103, "")
	if s.runtimeError() != "" {
		t.Fatal("recovered errors not cleared")
	}
}

func TestCatalogScheduleFailureCooldownAndNotify(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := setupSystemServiceTest(t)
		var broken atomic.Bool
		broken.Store(true)
		var queries atomic.Int64
		db.EntClient.KaguyaSystemInfo.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
			return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
				if fields := entbase.QueryFromContext(ctx); fields != nil && slices.Contains(fields.Fields, kaguyasysteminfo.FieldModelSyncIntervalHours) {
					queries.Add(1)
					if broken.Load() {
						return nil, errors.New("dsn=fake-secret")
					}
				}
				return next.Query(ctx, q)
			})
		}))
		s := NewModelCatalogSyncer(http.DefaultClient)
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); s.Run(runCtx) }()
		synctest.Wait()
		if queries.Load() != 1 || s.runtimeError() == "" {
			t.Fatal("scheduler failure was not visible")
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if queries.Load() != 1 {
			t.Fatal("busy failure loop")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if queries.Load() != 2 {
			t.Fatal("failed schedule did not retry at sixty seconds")
		}
		row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
		if err != nil || row.ModelSyncLastAttemptAt != nil {
			t.Fatal("scheduler read error was recorded as a download attempt")
		}
		broken.Store(false)
		s.Notify()
		synctest.Wait()
		if s.runtimeError() != "" {
			t.Fatal("recovered schedule did not clear error")
		}
		count := queries.Load()
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if queries.Load() != count {
			t.Fatal("disabled scheduler did not wait for notification")
		}
		cancel()
		synctest.Wait()
		<-done
	})
}

func TestCatalogSyncDatabaseFaultsAndRecovery(t *testing.T) {
	for _, mode := range []string{"url-query", "catalog-write", "failure-write", "network", "decode"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := setupSystemServiceTest(t)
				core, logs := observer.New(zap.DebugLevel)
				global.Logger = zap.New(core)
				client := db.EntClient
				if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetModelSyncEnabled(true).SetModelSyncIntervalHours(1).SetProviderSyncURL("https://fake.invalid/api?token=fake-secret").SetModelSyncURL("https://fake.invalid/models").SetModelCatalogJSON(`[{"id":"saved"}]`).SetProviderCatalogJSON(`[{"id":"saved"}]`).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				var failing atomic.Bool
				failing.Store(true)
				var attempts, downloads atomic.Int64
				cause := errors.New("dsn/header/response=fake-secret")
				client.KaguyaSystemInfo.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
					return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
						fields := entbase.QueryFromContext(ctx)
						if fields != nil && slices.Contains(fields.Fields, kaguyasysteminfo.FieldProviderSyncURL) && len(fields.Fields) == 3 {
							attempts.Add(1)
							if mode == "url-query" && failing.Load() {
								return nil, cause
							}
						}
						return next.Query(ctx, q)
					})
				}))
				client.KaguyaSystemInfo.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						_, catalog := m.Field(kaguyasysteminfo.FieldModelCatalogJSON)
						_, failure := m.Field(kaguyasysteminfo.FieldModelSyncLastError)
						if failing.Load() && (mode == "catalog-write" && catalog || mode == "failure-write" && failure) {
							return nil, cause
						}
						return next.Mutate(ctx, m)
					})
				})
				httpClient := &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
					downloads.Add(1)
					if failing.Load() && (mode == "network" || mode == "failure-write") {
						return nil, cause
					}
					body := `{"p":{"id":"p","name":"Provider","api":"https://fake.invalid"}}`
					if r.URL.Path == "/models" {
						body = `{"p/m":{"id":"p/m","name":"Model"}}`
					}
					if failing.Load() && mode == "decode" {
						body = `{"fake-secret":`
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
				})}
				s := NewModelCatalogSyncer(httpClient)
				old := DefaultModelCatalogSyncer
				DefaultModelCatalogSyncer = s
				defer func() { DefaultModelCatalogSyncer = old }()
				runCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); s.Run(runCtx) }()
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatalf("attempts=%d", attempts.Load())
				}
				info, err := (&SystemSvc{}).Info(ctx)
				if err != nil || info.ModelSyncLastError == "" || strings.Contains(info.ModelSyncLastError, "fake-secret") {
					t.Fatalf("unsafe/invisible error: %+v %v", info, err)
				}
				row, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
				if err != nil || row.ModelCatalogJSON != `[{"id":"saved"}]` || row.ProviderCatalogJSON != `[{"id":"saved"}]` || row.ModelSyncLastSuccessAt != nil {
					t.Fatal("failure overwrote successful catalog")
				}
				if (row.ModelSyncLastAttemptAt == nil) != (mode == "failure-write") {
					t.Fatal("failed attempt timestamp not persisted as expected")
				}
				if mode == "failure-write" && logs.FilterMessage("persist model catalog failure status failed").Len() != 1 {
					t.Fatal("failure-state write error was ignored")
				}
				time.Sleep(59 * time.Second)
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatal("failed synchronization retried before cooldown")
				}
				time.Sleep(time.Second)
				synctest.Wait()
				want := int64(1)
				if mode == "failure-write" {
					want = 2
				}
				if attempts.Load() != want {
					t.Fatalf("attempts at cooldown=%d want=%d", attempts.Load(), want)
				}
				if mode != "failure-write" {
					time.Sleep(59 * time.Minute)
					synctest.Wait()
					if attempts.Load() != 2 {
						t.Fatal("persisted failure did not wait full configured interval")
					}
				}
				failing.Store(false)
				// A single manual request succeeds even while the automatic retry is cooling down.
				if _, err := s.Sync(ctx); err != nil {
					t.Fatal(err)
				}
				if s.runtimeError() != "" {
					t.Fatal("successful retry did not clear sync error")
				}
				info, err = (&SystemSvc{}).Info(ctx)
				if err != nil || info.ModelSyncLastError != "" || info.ModelSyncCatalogCount != 1 {
					t.Fatal("recovered catalog not visible")
				}
				if strings.Contains(fmt.Sprint(logs.All()), "fake-secret") {
					t.Fatal("catalog logs exposed raw error")
				}
				cancel()
				synctest.Wait()
				<-done
			})
		})
	}
}
