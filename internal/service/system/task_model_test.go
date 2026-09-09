package system

import (
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
)

func TestTaskModelUniqueAndIndependent(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	p1, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "first", APIProtocol: consts.ProtocolOpenAIChat})
	if err != nil {
		t.Fatal(err)
	}
	if p1.ProviderType != consts.ProviderTypeNormal {
		t.Fatal("missing normal default")
	}
	p2, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{ProviderName: "second", APIProtocol: consts.ProtocolOpenAIChat, ProviderType: consts.ProviderTypeOpenCodeGo})
	if err != nil {
		t.Fatal(err)
	}
	if p2.ProviderType != consts.ProviderTypeOpenCodeGo {
		t.Fatal("provider type not saved")
	}
	r1 := modelSaveReq(p1.ID, "first", "same-api-id", consts.IsTrue)
	r1.IsTask = consts.IsTrue
	m1, err := svc.ModelCreate(ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	r2 := modelSaveReq(p2.ID, "second", "same-api-id", consts.IsFalse)
	r2.IsTask = consts.IsTrue
	m2, err := svc.ModelCreate(ctx, r2)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		rows, err := db.EntClient.KaguyaModelsInfo.Query().Where(kaguyamodelsinfo.IsTaskNotNil()).All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(rows) != 0 {
				t.Fatal("task slot not released")
			}
			return
		}
		if len(rows) != 1 || rows[0].ID != want {
			t.Fatalf("task models = %+v, want %s", rows, want)
		}
	}
	check(m2.ID)
	first, err := svc.ModelDetail(ctx, m1.ID)
	if err != nil || first.IsTask != consts.IsFalse || first.IsDefault != consts.IsTrue {
		t.Fatalf("default changed: %+v %v", first, err)
	}
	// 数据库约束阻止绕过服务层创建第二个任务模型。
	if err := db.EntClient.KaguyaModelsInfo.UpdateOneID(m1.ID).SetIsTask(consts.IsTrue).Exec(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("missing unique constraint: %v", err)
	}
	if _, err := svc.ModelUpdate(ctx, m1.ID, r1); err != nil {
		t.Fatal(err)
	}
	check(m1.ID)
	// 失败的创建必须回滚先前清除的任务标记。
	if _, err := svc.ModelCreate(ctx, r2); err == nil {
		t.Fatal("expected duplicate model failure")
	}
	check(m1.ID)
	r1.IsTask = consts.IsFalse
	if _, err := svc.ModelUpdate(ctx, m1.ID, r1); err != nil {
		t.Fatal(err)
	}
	check("")
	r1.IsTask = consts.IsTrue
	if _, err := svc.ModelUpdate(ctx, m1.ID, r1); err != nil {
		t.Fatal(err)
	}
	if err := svc.ModelDelete(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	check("")
	if _, err := svc.ModelUpdate(ctx, m2.ID, r2); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProviderDelete(ctx, p2.ID); err != nil {
		t.Fatal(err)
	}
	check("")
}
