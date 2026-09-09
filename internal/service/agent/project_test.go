package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
	project "github.com/lyonmu/kaguya/internal/service/project"
)

func TestProjectCRUDAndConversations(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := &project.ProjectSvc{}
	req := &dto.SaveReq{Name: " 项目一 ", Path: home, Description: "description"}
	p, err := svc.Save(ctx, "", req)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Name != "项目一" || p.Path != home {
		t.Fatalf("project=%+v", p)
	}
	for _, path := range []string{home, home + "/."} {
		if _, err := svc.Save(ctx, "", &dto.SaveReq{Name: "另一名称", Path: path}); !errors.Is(err, project.ErrPathExists) {
			t.Fatalf("duplicate path %s: %v", path, err)
		}
	}
	if _, err := client.KaguyaProject.Create().SetName("绕过服务校验").SetPath(home).Save(ctx); err == nil {
		t.Fatal("database accepted duplicate path")
	}
	other := filepath.Join(home, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	p2, err := svc.Save(ctx, "", &dto.SaveReq{Name: p.Name, Path: other})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"123", "124"} {
		turn := testCompletedTurn(id, 0)
		turn.ProjectID = p.ID
		if err := saveCompletedTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := client.KaguyaProject.QueryConversations(client.KaguyaProject.GetX(ctx, p.ID)).Count(ctx); err != nil || n != 2 {
		t.Fatalf("edge count=%d err=%v", n, err)
	}
	turn := testCompletedTurn("125", 0)
	turn.ProjectID = p2.ID
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := saveCompletedTurn(ctx, testCompletedTurn("126", 0)); err != nil {
		t.Fatal(err)
	}
	list, err := (&AgentSvc{}).ConversationPage(ctx, &dtochat.ConversationPageReq{ProjectID: p.ID, Page: 1, PageSize: 20})
	if err != nil || list.Total != 2 || !list.Items[0].IsProject || list.Items[0].ProjectID == nil || *list.Items[0].ProjectID != p.ID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	yes, no := true, false
	for _, filter := range []*bool{nil, &no} {
		ordinary, err := (&AgentSvc{}).ConversationPage(ctx, &dtochat.ConversationPageReq{IsProject: filter, Page: 1, PageSize: 1})
		if err != nil || ordinary.Total != 1 || len(ordinary.Items) != 1 || ordinary.Items[0].ID != "126" || ordinary.Items[0].IsProject {
			t.Fatalf("ordinary=%+v err=%v", ordinary, err)
		}
	}
	projects, err := (&AgentSvc{}).ConversationPage(ctx, &dtochat.ConversationPageReq{IsProject: &yes, Page: 1, PageSize: 2})
	if err != nil || projects.Total != 3 || len(projects.Items) != 2 {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	for _, item := range projects.Items {
		if !item.IsProject {
			t.Fatal("ordinary conversation in project list")
		}
	}
	if _, err := (&AgentSvc{}).ConversationPage(ctx, &dtochat.ConversationPageReq{ProjectID: p.ID, IsProject: &no, Page: 1, PageSize: 20}); !errors.Is(err, ErrConversationUpdate) {
		t.Fatalf("conflicting filters: %v", err)
	}
	if _, err := svc.Save(ctx, p2.ID, &dto.SaveReq{Name: p2.Name, Path: home}); !errors.Is(err, project.ErrPathExists) {
		t.Fatalf("duplicate update: %v", err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, "", &dto.SaveReq{Name: "链接", Path: alias}); !errors.Is(err, project.ErrPathExists) {
		t.Fatalf("symlink duplicate: %v", err)
	}
	req.Name = "重命名"
	req.Description = "updated"
	if _, err = svc.Save(ctx, p.ID, req); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.Detail(ctx, p.ID)
	if err != nil || detail.Name != req.Name || detail.Description != "updated" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	page, err := svc.Page(ctx, &dto.PageReq{Keyword: "重", Page: 1, PageSize: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := svc.Save(ctx, "", &dto.SaveReq{Name: " ", Path: home}); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("blank name: %v", err)
	}
	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Detail(ctx, p.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("deleted detail: %v", err)
	}
	if err := svc.Delete(ctx, p.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("repeat delete: %v", err)
	}
	for _, id := range []string{"123", "124"} {
		conv, err := (&AgentSvc{}).ConversationDetail(ctx, id)
		if err != nil || conv.IsProject || conv.ProjectID != nil || conv.TurnCount != 1 {
			t.Fatalf("preserve=%+v err=%v", conv, err)
		}
	}
	// A generation started before project deletion cannot attach to a deleted project.
	turn = testCompletedTurn("127", 0)
	turn.ProjectID = p.ID
	if err := saveCompletedTurn(ctx, turn); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("deleted attachment: %v", err)
	}
	if n, err := client.KaguyaConversation.Query().Count(ctx); err != nil || n != 4 {
		t.Fatalf("rollback count=%d err=%v", n, err)
	}
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 1)); err != nil {
		t.Fatalf("continue detached: %v", err)
	}
	if _, err := svc.Save(ctx, "", &dto.SaveReq{Name: "重新创建", Path: home}); err != nil {
		t.Fatalf("reuse deleted project path: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("host directory removed: %v", err)
	}
}
