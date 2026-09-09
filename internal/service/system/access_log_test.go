package system

import (
	"testing"

	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestAccessLogUnixSecondRange(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	for _, second := range []int64{1735689599, 1735689600, 1735689601, 1735689602} {
		if err := db.EntClient.KaguyaAccessLog.Create().SetAccessTime(second).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		start, end int64
		count      int
	}{
		{1735689600, 1735689601, 2}, {1735689600, 1735689600, 1},
		{0, 1735689600, 2}, {1735689601, 0, 2}, {0, 0, 4},
	} {
		got, err := (&SystemSvc{}).AccessLogPage(ctx, &dtosystem.SystemAccessLogPageReq{StartTime: tc.start, EndTime: tc.end, Page: 1, PageSize: 10})
		if err != nil || got.Total != tc.count {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
	}
	for _, req := range []dtosystem.SystemAccessLogPageReq{{StartTime: -1}, {EndTime: 1735689600000}, {StartTime: 200, EndTime: 100}} {
		if _, err := (&SystemSvc{}).AccessLogPage(ctx, &req); err != dtosystem.ErrInvalidTimeRange {
			t.Fatalf("%+v: %v", req, err)
		}
	}
}
