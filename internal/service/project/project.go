package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproject"
	"github.com/lyonmu/kaguya/internal/global"
)

var ErrPathExists = errors.New("project directory already exists")
var ErrNotFound = errors.New("project not found")
var ErrInvalid = errors.New("invalid project or directory outside home")

type ProjectSvc struct{}

func within(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Resolve both the home and selected path before checking containment, including symlinks.
func resolveDirectory(path string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	originalHome := home
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", "", err
	}
	if path == "" {
		path = home
	}
	if !filepath.IsAbs(path) || (!within(home, filepath.Clean(path)) && !within(originalHome, filepath.Clean(path))) {
		return "", "", ErrInvalid
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	if !within(home, path) {
		return "", "", ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", ErrInvalid
	}
	return home, path, nil
}

// Open relative to a pinned home handle so a concurrent symlink replacement cannot
// make directory enumeration read outside the allowed root.
func openDirectory(home, path string) (*os.File, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalid
	}
	return f, nil
}

func (s *ProjectSvc) Directories(ctx context.Context, path string) (*dto.DirectoryResp, error) {
	home, path, err := resolveDirectory(path)
	if err != nil {
		return nil, err
	}
	f, err := openDirectory(home, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	resp := &dto.DirectoryResp{Home: home, Path: path, Items: []dto.Directory{}}
	if path != home {
		resp.Parent = filepath.Dir(path)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Do not offer symlinks: folder navigation cannot escape through a link.
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			resp.Items = append(resp.Items, dto.Directory{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
		}
	}
	global.Logger.Sugar().Debugf("list project directories: path=%s count=%d", path, len(resp.Items))
	return resp, nil
}
func response(row *ent.KaguyaProject) *dto.Resp {
	return &dto.Resp{ID: row.ID, Name: row.Name, Path: row.Path, Description: row.Description, CreatedAt: row.CreatedAt}
}

// Workspace revalidates the directory on every chat turn; missing or moved
// projects must fail explicitly, never fall back to the server working directory.
func (s *ProjectSvc) Workspace(ctx context.Context, id string) (string, error) {
	project, err := s.Detail(ctx, id)
	if err != nil {
		return "", err
	}
	_, path, err := resolveDirectory(project.Path)
	return path, err
}

func (s *ProjectSvc) Detail(ctx context.Context, id string) (*dto.Resp, error) {
	row, err := db.EntClient.KaguyaProject.Query().Where(kaguyaproject.IDEQ(id), kaguyaproject.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return response(row), nil
}
func (s *ProjectSvc) Page(ctx context.Context, req *dto.PageReq) (*dto.PageResp, error) {
	if req.Page < 1 || req.PageSize < 1 || req.PageSize > 100 {
		return nil, ErrInvalid
	}
	q := db.EntClient.KaguyaProject.Query().Where(kaguyaproject.DeletedAtIsNil(), kaguyaproject.NameHasPrefix(req.Keyword))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Order(kaguyaproject.ByName(), kaguyaproject.ByID()).Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dto.PageResp{Items: []dto.Resp{}, Total: total, Page: req.Page, PageSize: req.PageSize}
	for _, row := range rows {
		resp.Items = append(resp.Items, *response(row))
	}
	return resp, nil
}
func (s *ProjectSvc) Save(ctx context.Context, id string, req *dto.SaveReq) (*dto.Resp, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 200 || len([]rune(req.Description)) > 2000 || req.Path == "" || len(req.Path) > 4096 {
		return nil, ErrInvalid
	}
	home, path, err := resolveDirectory(req.Path)
	if err != nil {
		return nil, err
	}
	// Verify that the directory is readable by the running user.
	f, err := openDirectory(home, path)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	exists, err := db.EntClient.KaguyaProject.Query().Where(kaguyaproject.PathEQ(path), kaguyaproject.DeletedAtIsNil(), kaguyaproject.IDNEQ(id)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrPathExists
	}
	var row *ent.KaguyaProject
	if id == "" {
		row, err = db.EntClient.KaguyaProject.Create().SetName(name).SetPath(path).SetDescription(req.Description).Save(ctx)
	} else {
		row, err = db.EntClient.KaguyaProject.UpdateOneID(id).Where(kaguyaproject.DeletedAtIsNil()).SetName(name).SetPath(path).SetDescription(req.Description).Save(ctx)
	}
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if ent.IsConstraintError(err) {
		return nil, ErrPathExists
	}
	if err != nil {
		return nil, err
	}
	global.Logger.Sugar().Infof("save project: id=%s path=%s", row.ID, row.Path)
	return response(row), nil
}

// Lock serializes new conversation attachment with project deletion across instances.
// Caller must hold a transaction until its conversation changes are committed.
func Lock(ctx context.Context, client *ent.Client, id string) error {
	n, err := client.KaguyaProject.Update().Where(kaguyaproject.IDEQ(id), kaguyaproject.DeletedAtIsNil()).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *ProjectSvc) Delete(ctx context.Context, id string) error {
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = Lock(ctx, tx.Client(), id); err != nil {
		return err
	}
	if _, err = tx.KaguyaConversation.Update().Where(kaguyaconversation.ProjectIDEQ(id)).ClearProjectID().Save(ctx); err != nil {
		return err
	}
	if err = tx.KaguyaProject.UpdateOneID(id).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	global.Logger.Sugar().Infof("delete project, preserve conversations and host files: id=%s", id)
	return nil
}
