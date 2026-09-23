package cmd

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
)

type runtimeCloseDriver struct {
	dialect.Driver
	closes atomic.Int64
}

func (d *runtimeCloseDriver) Close() error { d.closes.Add(1); return d.Driver.Close() }

func TestRuntimeDrainsBackgroundPersistenceBeforeClosingDatabase(t *testing.T) {
	rt := newAppRuntime(context.Background())
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	driver := &runtimeCloseDriver{Driver: entsql.OpenDB(dialect.SQLite, raw)}
	old := db.EntClient
	db.EntClient = ent.NewClient(ent.Driver(driver))
	defer func() { db.EntClient = old }()
	rt.dbReady, rt.modelSyncUp = true, true
	release, closed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseWork := func() { once.Do(func() { close(release) }) }
	defer func() { releaseWork(); <-closed }()
	persisted := make(chan error, 1)
	go func() {
		<-rt.ctx.Done()
		<-release
		_, err := raw.Exec("CREATE TABLE drained (id INTEGER)")
		persisted <- err
		close(rt.modelSyncDone)
	}()
	go func() { rt.close(); close(closed) }()
	<-rt.ctx.Done()
	select {
	case <-closed:
		t.Fatal("closed before background drain")
	case <-time.After(10 * time.Millisecond):
	}
	if driver.closes.Load() != 0 {
		t.Fatal("database closed while final persistence was pending")
	}
	if err := raw.Ping(); err != nil {
		t.Fatal(err)
	}
	releaseWork()
	<-closed
	if err := <-persisted; err != nil {
		t.Fatal("delayed persistence failed", err)
	}
	rt.close()
	if driver.closes.Load() != 1 {
		t.Fatal("database must close exactly once")
	}
	if err := raw.Ping(); err == nil {
		t.Fatal("database remained open")
	}
}

func TestAppRuntimeShutdownClosesAdmission(t *testing.T) {
	rt := newAppRuntime(context.Background())
	defer rt.close()

	if !rt.Admission().Enter() {
		t.Fatal("runtime rejected a request before shutdown")
	}
	rt.Admission().Leave()

	rt.beginShutdown()
	if rt.Admission().Enter() {
		t.Fatal("runtime admitted a request after shutdown started")
	}
	if err := rt.RootContext().Err(); err == nil {
		t.Fatal("shutdown did not cancel the root context")
	}
}

func TestAppRuntimeCloseIsIdempotent(t *testing.T) {
	rt := newAppRuntime(context.Background())
	rt.beginShutdown()
	rt.close()
	rt.close()
	if err := rt.RootContext().Err(); err == nil {
		t.Fatal("close did not cancel the root context")
	}
}

func TestAdmissionHandlerRejectsAfterStop(t *testing.T) {
	rt := newAppRuntime(context.Background())
	defer rt.close()
	served := false
	handler := rt.admissionHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/kaguya/api/v1/system/info", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || !served {
		t.Fatalf("status=%d served=%v", w.Code, served)
	}

	rt.beginShutdown()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after stop=%d", w.Code)
	}
}

func TestAdmissionHandlerWaitsForInflight(t *testing.T) {
	rt := newAppRuntime(context.Background())
	defer rt.close()

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := rt.admissionHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://localhost/kaguya/api/v1/chat/sse", nil))
	}()
	<-entered
	rt.beginShutdown()
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	if err := rt.gate.Wait(shortCtx); err == nil {
		t.Fatal("wait returned while a handler was still running")
	}
	close(release)
	<-done
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.gate.Wait(waitCtx); err != nil {
		t.Fatalf("wait after handler exit: %v", err)
	}
}
