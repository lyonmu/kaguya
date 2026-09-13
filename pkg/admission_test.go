package pkg

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAdmissionStopsNewRequests(t *testing.T) {
	admission := NewAdmission()
	if !admission.Enter() {
		t.Fatal("first request rejected")
	}
	admission.Stop()
	if admission.Enter() {
		t.Fatal("request admitted after Stop")
	}
	admission.Leave()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Wait(ctx); err != nil {
		t.Fatalf("wait after drain: %v", err)
	}
}

func TestAdmissionWaitsForInflightRequests(t *testing.T) {
	admission := NewAdmission()
	if !admission.Enter() {
		t.Fatal("request rejected")
	}
	admission.Stop()

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	done := make(chan error, 1)
	go func() { done <- admission.Wait(waitCtx) }()

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if err := admission.Wait(shortCtx); err == nil {
		t.Fatal("wait returned before the request finished")
	}
	admission.Leave()
	if err := <-done; err != nil {
		t.Fatalf("wait after leave: %v", err)
	}
}

// TestAdmissionConcurrentEnter 在释放审计下验证准入计数与 Stop 的互斥。
func TestAdmissionConcurrentEnter(t *testing.T) {
	admission := NewAdmission()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if admission.Enter() {
				time.Sleep(time.Millisecond)
				admission.Leave()
			}
		}()
	}
	time.Sleep(2 * time.Millisecond)
	admission.Stop()
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admission.Wait(ctx); err != nil {
		t.Fatalf("wait after concurrent use: %v", err)
	}
}
