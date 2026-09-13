package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
