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
	// 目录条目上限，防止异常响应把整份 JSON 写进单行配置。
	modelCatalogMaxEntries    = 10000
	providerCatalogMaxEntries = 1000
)

var ErrModelCatalogSync = errors.New("model catalog sync failed")

// catalogProviderSource 对应提供商目录 api.json 顶层的单个提供商；模型内嵌在其 models 下。
type catalogProviderSource struct {
	ID     string                        `json:"id"`
	Name   string                        `json:"name"`
	API    string                        `json:"api"`
	NPM    string                        `json:"npm"`
	Doc    string                        `json:"doc"`
	Models map[string]modelCatalogSource `json:"models"`
}

// modelCatalogSource 对应模型目录 models.json 中的单个模型；键为 提供商/模型标识。
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

// Sync 分别下载提供商目录（api.json）与模型目录（models.json），两个目录一旦有一边失败
// 就整体失败，避免出现半新半旧的配置。
func (s *ModelCatalogSyncer) Sync(ctx context.Context) (*dtosystem.SystemModelSyncResp, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	attemptedAt := time.Now()
	config, err := db.EntClient.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldID, kaguyasysteminfo.FieldProviderSyncURL, kaguyasysteminfo.FieldModelSyncURL).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: query sync URL: %v", ErrModelCatalogSync, err)
	}
	providerBody, err := s.download(ctx, config.ProviderSyncURL)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("provider catalog: %w", err))
	}
	modelBody, err := s.download(ctx, config.ModelSyncURL)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("model catalog: %w", err))
	}

	var source map[string]catalogProviderSource
	if err := json.Unmarshal(providerBody, &source); err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	if len(source) == 0 || len(source) > providerCatalogMaxEntries {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid provider count %d", len(source)))
	}
	providers := make([]*dtosystem.SystemProviderCatalogResp, 0, len(source))
	providerNames := make(map[string]string, len(source))
	for providerID, provider := range source {
		if provider.ID != providerID || provider.Name == "" || len(provider.ID) > 256 || len(provider.Name) > 256 {
			return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid provider entry %q", providerID))
		}
		providerNames[providerID] = provider.Name
		// 没有 api 根地址的提供商无法用于本地配置，只保留名称供模型目录使用。
		if strings.TrimSpace(provider.API) == "" {
			continue
		}
		providers = append(providers, &dtosystem.SystemProviderCatalogResp{
			ID: providerID, Name: provider.Name, API: provider.API, NPM: provider.NPM, Doc: provider.Doc,
			ModelCount: len(provider.Models),
		})
	}

	var modelSource map[string]modelCatalogSource
	if err := json.Unmarshal(modelBody, &modelSource); err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	if len(modelSource) == 0 || len(modelSource) > modelCatalogMaxEntries {
		return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid model count %d", len(modelSource)))
	}
	models := make([]*dtosystem.SystemModelCatalogResp, 0, len(modelSource))
	for key, item := range modelSource {
		providerID, modelID, ok := strings.Cut(key, "/")
		if !ok || providerID == "" || modelID == "" || item.ID != key || item.Name == "" || len(item.ID) > 256 || len(item.Name) > 256 || item.Limit.Context < 0 || item.Limit.Output < 0 {
			return nil, s.fail(ctx, attemptedAt, fmt.Errorf("invalid model entry %q", key))
		}
		providerName := providerNames[providerID]
		if providerName == "" {
			providerName = providerID
		}
		models = append(models, &dtosystem.SystemModelCatalogResp{
			ID: key, ProviderID: providerID, ProviderName: providerName, ModelID: modelID,
			Name: item.Name, Family: item.Family, Description: item.Description,
			ReasoningEnabled:   itemStatus(item.Reasoning),
			TokenContextWindow: item.Limit.Context, TokenMaxOutputTokens: item.Limit.Output,
			CapabilityToolUse: itemStatus(item.ToolCall), CapabilityVision: itemStatus(contains(item.Modalities.Input, "image")),
			CapabilityStructuredOutput: itemStatus(item.StructuredOutput), InputModalities: item.Modalities.Input,
			ReleaseDate: item.ReleaseDate, LastUpdated: item.LastUpdated,
		})
	}
	sortModelCatalog(models)
	sortProviderCatalog(providers)
	modelJSON, err := json.Marshal(models)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	providerJSON, err := json.Marshal(providers)
	if err != nil {
		return nil, s.fail(ctx, attemptedAt, err)
	}
	syncedAt := time.Now()
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetModelCatalogJSON(string(modelJSON)).
		SetModelCatalogCount(len(models)).
		SetProviderCatalogJSON(string(providerJSON)).
		SetProviderCatalogCount(len(providers)).
		SetModelSyncLastAttemptAt(attemptedAt).
		SetModelSyncLastSuccessAt(syncedAt).
		SetModelSyncLastError("").
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("%w: persist catalog: %v", ErrModelCatalogSync, err)
	}
	global.Logger.Sugar().Infof("catalog synced: providers=%d models=%d", len(providers), len(models))
	return &dtosystem.SystemModelSyncResp{Count: len(models), ProviderCount: len(providers), SyncedAt: syncedAt}, nil
}

// download 按配置地址下载一份目录 JSON，限制大小并抑制 User-Agent。
func (s *ModelCatalogSyncer) download(ctx context.Context, rawURL string) ([]byte, error) {
	if validateModelSyncURL(rawURL) != nil {
		return nil, errors.New("invalid configured sync URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header["User-Agent"] = nil
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelCatalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > modelCatalogMaxBytes {
		return nil, errors.New("response exceeds 8 MiB")
	}
	return body, nil
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
		if keyword == "" || strings.Contains(strings.ToLower(item.Name), keyword) || strings.Contains(strings.ToLower(item.ID), keyword) || strings.Contains(strings.ToLower(item.ProviderName), keyword) || strings.Contains(strings.ToLower(item.Family), keyword) || strings.Contains(strings.ToLower(item.Description), keyword) {
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

// ProviderCatalog 查询已同步的提供商目录；只包含带 api 根地址的提供商。
func (s *SystemSvc) ProviderCatalog(ctx context.Context, req *dtosystem.SystemProviderCatalogReq) (*dtosystem.SystemProviderCatalogListResp, error) {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return nil, err
	}
	var all []*dtosystem.SystemProviderCatalogResp
	if err := json.Unmarshal([]byte(row.ProviderCatalogJSON), &all); err != nil {
		return nil, fmt.Errorf("decode provider catalog: %w", err)
	}
	keyword := strings.ToLower(strings.TrimSpace(req.Keyword))
	items := make([]*dtosystem.SystemProviderCatalogResp, 0, len(all))
	for _, item := range all {
		if keyword == "" || strings.Contains(strings.ToLower(item.Name), keyword) || strings.Contains(strings.ToLower(item.ID), keyword) || strings.Contains(strings.ToLower(item.NPM), keyword) || strings.Contains(strings.ToLower(item.API), keyword) {
			items = append(items, item)
		}
	}
	sortProviderCatalog(items)
	total := len(items)
	start := (req.Page - 1) * req.PageSize
	if start > total {
		start = total
	}
	end := start + req.PageSize
	if end > total {
		end = total
	}
	return &dtosystem.SystemProviderCatalogListResp{Total: total, Items: items[start:end], Page: req.Page, PageSize: req.PageSize}, nil
}

// sortProviderCatalog 按名称 A→Z 排序；名称相同再按 ID，保证顺序稳定。
func sortProviderCatalog(items []*dtosystem.SystemProviderCatalogResp) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if left != right {
			return left < right
		}
		return items[i].ID < items[j].ID
	})
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
