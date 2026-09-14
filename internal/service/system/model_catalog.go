package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/internal/global"
)

const (
	modelCatalogMaxBytes = 8 << 20
	modelCatalogTimeout  = 30 * time.Second
)

var ErrModelCatalogSync = errors.New("model catalog sync failed")

type modelCatalogSource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Family           string `json:"family"`
	Description      string `json:"description"`
	Reasoning        bool   `json:"reasoning"`
	ToolCall         bool   `json:"tool_call"`
	StructuredOutput bool   `json:"structured_output"`
	ReleaseDate      string `json:"release_date"`
	LastUpdated      string `json:"last_updated"`
	Modalities       struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// ModelCatalogSyncer 负责手动同步与定时调度；两条路径共享互斥锁，避免重复下载覆盖。
type ModelCatalogSyncer struct {
	client *http.Client
	notify chan struct{}
	syncMu sync.Mutex
}

var DefaultModelCatalogSyncer = NewModelCatalogSyncer(newModelCatalogHTTPClient())

func NewModelCatalogSyncer(client *http.Client) *ModelCatalogSyncer {
	return &ModelCatalogSyncer{client: client, notify: make(chan struct{}, 1)}
}

func newModelCatalogHTTPClient() *http.Client {
	return &http.Client{
		Timeout: modelCatalogTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if validateModelSyncURL(req.URL.String()) != nil {
				return errors.New("invalid redirect URL")
			}
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("model catalog redirect cannot downgrade HTTPS")
			}
			req.Header["User-Agent"] = nil
			return nil
		},
	}
}

// Notify 让调度器立即重新读取持久化配置。
func (s *ModelCatalogSyncer) Notify() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Run 按最近尝试时间计算下一次同步；失败后同样等待完整间隔，避免网络异常时忙重试。
func (s *ModelCatalogSyncer) Run(ctx context.Context) {
	for {
		row, err := db.EntClient.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
			Select(kaguyasysteminfo.FieldID, kaguyasysteminfo.FieldModelSyncEnabled, kaguyasysteminfo.FieldModelSyncIntervalHours, kaguyasysteminfo.FieldModelSyncLastAttemptAt).
			Only(ctx)
		if err != nil {
			if ctx.Err() == nil {
				global.Logger.Sugar().Errorf("query model sync schedule failed: %v", err)
			}
			return
		}
		if !row.ModelSyncEnabled {
			select {
			case <-ctx.Done():
				return
			case <-s.notify:
				continue
			}
		}

		delay := time.Duration(0)
		if row.ModelSyncLastAttemptAt != nil {
			delay = time.Until(row.ModelSyncLastAttemptAt.Add(time.Duration(row.ModelSyncIntervalHours) * time.Hour))
		}
		if delay <= 0 {
			if _, err := s.Sync(ctx); err != nil && ctx.Err() == nil {
				global.Logger.Sugar().Warnf("scheduled model catalog sync failed: %v", err)
			}
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-s.notify:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (s *ModelCatalogSyncer) Sync(ctx context.Context) (*dtosystem.SystemModelSyncResp, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	attemptedAt := time.Now()
	config, err := db.EntClient.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldID, kaguyasysteminfo.FieldModelSyncURL).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: query sync URL: %v", ErrModelCatalogSync, err)
	}
	if validateModelSyncURL(config.ModelSyncURL) != nil {
		return nil, s.fail(ctx, attemptedAt, errors.New("invalid configured sync URL"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.ModelSyncURL, nil)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	// 显式抑制 net/http 默认值，模型目录同步同样不附加 User-Agent。
	req.Header["User-Agent"] = nil
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode))
	}

	limited := io.LimitReader(resp.Body, modelCatalogMaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	if len(body) > modelCatalogMaxBytes {
		return nil, s.fail(ctx, attemptedAt, errors.New("response exceeds 8 MiB"))
	}
	var source map[string]modelCatalogSource
	if err := json.Unmarshal(body, &source); err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	if len(source) == 0 || len(source) > 10000 {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid model count %d", len(source)))
	}

	items := make([]*dtosystem.SystemModelCatalogResp, 0, len(source))
	for key, item := range source {
		if item.ID == "" || item.Name == "" || item.ID != key || len(item.ID) > 256 || len(item.Name) > 256 || item.Limit.Context < 0 || item.Limit.Output < 0 {
			return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid model entry %q", key))
		}
		items = append(items, &dtosystem.SystemModelCatalogResp{
			ID: item.ID, Name: item.Name, Lab: modelLab(item.ID), Family: item.Family, Description: item.Description,
			ReasoningEnabled:   itemStatus(item.Reasoning),
			TokenContextWindow: item.Limit.Context, TokenMaxOutputTokens: item.Limit.Output,
			CapabilityToolUse: itemStatus(item.ToolCall), CapabilityVision: itemStatus(contains(item.Modalities.Input, "image")),
			CapabilityStructuredOutput: itemStatus(item.StructuredOutput), InputModalities: item.Modalities.Input,
			ReleaseDate: item.ReleaseDate, LastUpdated: item.LastUpdated,
		})
	}
	sortModelCatalog(items)
	catalogJSON, err := json.Marshal(items)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	syncedAt := time.Now()
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetModelCatalogJSON(string(catalogJSON)).
		SetModelCatalogCount(len(items)).
		SetModelSyncLastAttemptAt(attemptedAt).
		SetModelSyncLastSuccessAt(syncedAt).
		SetModelSyncLastError("").
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("%w: persist catalog: %v", ErrModelCatalogSync, err)
	}
	global.Logger.Sugar().Infof("model catalog synced: count=%d", len(items))
	return &dtosystem.SystemModelSyncResp{Count: len(items), SyncedAt: syncedAt}, nil
}

func (s *ModelCatalogSyncer) fail(ctx context.Context, attemptedAt time.Time, cause error) error {
	var urlError *url.Error
	if errors.As(cause, &urlError) {
		cause = urlError.Err
	}
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	_ = db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetModelSyncLastAttemptAt(attemptedAt).
		SetModelSyncLastError(message).
		Exec(ctx)
	return fmt.Errorf("%w: %v", ErrModelCatalogSync, cause)
}

func (s *SystemSvc) ModelCatalog(ctx context.Context, req *dtosystem.SystemModelCatalogReq) (*dtosystem.SystemModelCatalogListResp, error) {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return nil, err
	}
	var all []*dtosystem.SystemModelCatalogResp
	if err := json.Unmarshal([]byte(row.ModelCatalogJSON), &all); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	keyword := strings.ToLower(strings.TrimSpace(req.Keyword))
	items := make([]*dtosystem.SystemModelCatalogResp, 0, len(all))
	for _, item := range all {
		if keyword == "" || strings.Contains(strings.ToLower(item.Name), keyword) || strings.Contains(strings.ToLower(item.ID), keyword) || strings.Contains(strings.ToLower(item.Lab), keyword) || strings.Contains(strings.ToLower(item.Family), keyword) || strings.Contains(strings.ToLower(item.Description), keyword) {
			items = append(items, item)
		}
	}
	sortModelCatalog(items)
	total := len(items)
	start := (req.Page - 1) * req.PageSize
	if start > total {
		start = total
	}
	end := start + req.PageSize
	if end > total {
		end = total
	}
	return &dtosystem.SystemModelCatalogListResp{Total: total, Items: items[start:end], Page: req.Page, PageSize: req.PageSize}, nil
}

func itemStatus(value bool) consts.Status {
	if value {
		return consts.IsTrue
	}
	return consts.IsFalse
}

func modelLab(id string) string {
	lab, _, _ := strings.Cut(id, "/")
	return lab
}

// sortModelCatalog 按发布日期降序；缺失或相同发布日期时再按更新时间、名称和 ID。
func sortModelCatalog(items []*dtosystem.SystemModelCatalogResp) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.ReleaseDate != right.ReleaseDate {
			return left.ReleaseDate > right.ReleaseDate
		}
		if left.LastUpdated != right.LastUpdated {
			return left.LastUpdated > right.LastUpdated
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
