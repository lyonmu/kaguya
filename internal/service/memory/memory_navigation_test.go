package memory

import (
	"errors"
	"strings"
	"testing"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// 来源导航：Memory 证据 → 来源 → 会话/轮次/片段可读定位。
func TestSourceDetailTurnNavigation(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-nav", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "用户陈述：继续使用 SQLCipher。", "助手回答。")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.SourceDetail(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Available || detail.ConversationID != conv || detail.TurnID != turnID ||
		detail.TurnStatus != "completed" {
		t.Fatalf("detail=%+v", detail)
	}
	if len(detail.Parts) < 2 || detail.Parts[0].PartKey != "user" || detail.Parts[0].Origin != OriginUserStatement {
		t.Fatalf("parts=%+v", detail.Parts)
	}
	if !strings.Contains(detail.Parts[0].Text, "继续使用 SQLCipher") {
		t.Fatalf("part text=%q", detail.Parts[0].Text)
	}
}

// 原始轮次被删除后来源导航明确标记不可用，而不是静默为空。
func TestSourceDetailDeletedTurn(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-gone", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "会被删除的轮次")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaChatTurn.Delete().Where(kaguyachatturn.IDEQ(turnID)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.SourceDetail(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Available || detail.UnavailableReason == "" || len(detail.Parts) != 0 {
		t.Fatalf("detail=%+v", detail)
	}
}

// 已撤销来源（会话删除或隐私关闭）不可导航到原始内容。
func TestSourceDetailExcludedSource(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-excluded", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "撤销来源")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaMemorySource.UpdateOneID(src.ID).
		SetState(kaguyamemorysource.StateExcluded).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.SourceDetail(ctx, src.ID)
	if err != nil || detail.Available || detail.UnavailableReason == "" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
}

// 人工笔记来源可导航到页面正文。
func TestSourceDetailNoteNavigation(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "preference", Title: "回答偏好",
		Summary: "使用中文", Body: "始终使用中文回答。",
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.SourceKeyEQ("note:" + page.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.SourceDetail(ctx, src.ID)
	if err != nil || !detail.Available || len(detail.Parts) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if detail.Parts[0].PartKey != "note" || !strings.Contains(detail.Parts[0].Text, "始终使用中文回答") {
		t.Fatalf("parts=%+v", detail.Parts)
	}
}

// 不存在的来源明确返回错误。
func TestSourceDetailNotFound(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	if _, err := svc.SourceDetail(ctx, "missing"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err=%v", err)
	}
}
