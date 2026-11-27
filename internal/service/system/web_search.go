package system

import (
	"context"
	"errors"
	"strings"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/internal/secret"
)

var errWebSearchSecret = errors.New("web search API key cannot be encrypted or decrypted")

func loadWebSearchConfig(ctx context.Context) (*ent.KaguyaSystemInfo, error) {
	return db.EntClient.KaguyaSystemInfo.Query().
		Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldWebSearchEnabled, kaguyasysteminfo.FieldWebSearchAPIKey).
		Only(ctx)
}

func webSearchConfig(row *ent.KaguyaSystemInfo) *dtosystem.WebSearchConfig {
	return &dtosystem.WebSearchConfig{
		Enabled: row.WebSearchEnabled, KeyConfigured: secret.IsEncrypted(row.WebSearchAPIKey),
	}
}

func (s *SystemSvc) WebSearchConfig(ctx context.Context) (*dtosystem.WebSearchConfig, error) {
	row, err := loadWebSearchConfig(ctx)
	if err != nil {
		return nil, err
	}
	return webSearchConfig(row), nil
}

func (s *SystemSvc) UpdateWebSearchConfig(ctx context.Context, req *dtosystem.WebSearchSaveReq) (*dtosystem.WebSearchConfig, error) {
	key := strings.TrimSpace(req.APIKey)
	if req.Enabled == nil || len(key) > 2048 || strings.ContainsFunc(key, func(r rune) bool { return r < '!' || r > '~' }) {
		return nil, ErrInvalidSystemInfo
	}

	update := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetWebSearchEnabled(*req.Enabled)
	if key != "" {
		ciphertext, err := secret.Encrypt(key)
		if err != nil || !secret.IsEncrypted(ciphertext) {
			return nil, errWebSearchSecret
		}
		update.SetWebSearchAPIKey(ciphertext)
	} else if *req.Enabled {
		update.Where(kaguyasysteminfo.WebSearchAPIKeyHasPrefix(secret.CiphertextPrefixV2))
	}

	row, err := update.Select(kaguyasysteminfo.FieldWebSearchEnabled, kaguyasysteminfo.FieldWebSearchAPIKey).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) && *req.Enabled && key == "" {
			return nil, ErrInvalidSystemInfo
		}
		return nil, err
	}
	return webSearchConfig(row), nil
}
