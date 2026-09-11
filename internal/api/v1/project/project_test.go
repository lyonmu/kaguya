package project

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/db"
	code "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

type testID struct{ n atomic.Int64 }

func (g *testID) GenID() (int64, error) { return g.n.Add(1), nil }
func TestProjectAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	client, err := ent.Open(dialect.SQLite, "file:project-api?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = client, &testID{}, zap.NewNop()
	defer func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger }()
	r := gin.New()
	api := &ProjectApiV1Group{}
	r.GET("/project/page", api.ProjectPage)
	r.GET("/project/directories", api.ProjectDirectories)
	r.GET("/project/:id/tree", api.ProjectTree)
	r.GET("/project/:id/content", api.ProjectContent)
	r.GET("/project/:id/git/status", api.ProjectGitStatus)
	r.GET("/project/:id/git/diff", api.ProjectGitDiff)
	r.GET("/project/:id", api.ProjectDetail)
	r.POST("/project", api.ProjectCreate)
	r.PUT("/project/:id", api.ProjectUpdate)
	r.DELETE("/project/:id", api.ProjectDelete)
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var result struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Code != want {
			t.Fatalf("%s %s: %s", method, path, w.Body.String())
		}
		return result.Data
	}
	payload, _ := json.Marshal(map[string]string{"name": "项目", "path": home, "description": "测试"})
	created := request("POST", "/project", string(payload), code.SystemSuccess.Code)
	id := created["id"].(string)
	request("POST", "/project", string(payload), code.ProjectPathInUse.Code)
	request("GET", "/project/"+id, "", code.SystemSuccess.Code)
	request("PUT", "/project/"+id, string(payload), code.SystemSuccess.Code)
	page := request("GET", "/project/page", "", code.SystemSuccess.Code)
	if page["total"] != float64(1) {
		t.Fatalf("page=%+v", page)
	}
	request("GET", "/project/directories", "", code.SystemSuccess.Code)
	if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request("GET", "/project/"+id+"/tree", "", code.SystemSuccess.Code)
	request("GET", "/project/"+id+"/content?path=note.txt", "", code.SystemSuccess.Code)
	request("GET", "/project/"+id+"/content?path=..%2Fescape", "", code.ProjectParameterError.Code)
	request("GET", "/project/"+id+"/content", "", code.RequestParameterError.Code)
	request("GET", "/project/"+id+"/git/status", "", code.SystemSuccess.Code)
	request("GET", "/project/"+id+"/git/diff?path=note.txt", "", code.ProjectNotGit.Code)
	request("GET", "/project/"+id+"/git/diff", "", code.RequestParameterError.Code)
	request("GET", "/project/directories?path=/", "", code.ProjectParameterError.Code)
	request("GET", "/project/page?page=0", "", code.RequestParameterError.Code)
	request("POST", "/project", `{"name":"x"}`, code.RequestParameterError.Code)
	request("DELETE", "/project/"+id, "", code.SystemSuccess.Code)
	request("GET", "/project/"+id, "", code.ProjectNotFound.Code)
}
