package agent

import (
	"context"
	"errors"
	"testing"
	"time"

)

func TestWaitActive(t *testing.T) {
	done, ok := startWork()
	if !ok {
		t.Fatal("work admission rejected before shutdown")
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelWait()
	if err := WaitActive(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	done()
	if err := WaitActive(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
}

// 关停后拒绝新任务；已登记任务完成前 Wait 不返回，完成后立即返回。
// 使用独立实例，不污染包级生命周期。
func TestWorkLifecycleStopsAdmissionAndDrains(t *testing.T) {
	var lifecycle workLifecycle
	finish, ok := lifecycle.start()
	if !ok {
		t.Fatal("work admission rejected before shutdown")
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		if err := lifecycle.wait(context.Background()); err != nil {
			t.Errorf("wait active: %v", err)
		}
	}()
	lifecycle.stop()
	if _, ok := lifecycle.start(); ok {
		t.Fatal("stopping lifecycle must reject new work")
	}
	select {
	case <-drained:
		t.Fatal("wait returned before in-flight work finished")
	default:
	}
	finish()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("wait did not return after work drained")
	}
	if got := lifecycle.pending(); got != 0 {
		t.Fatalf("pending=%d", got)
	}
}
