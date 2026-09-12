package system

import (
	"context"

	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
)

// InitSecret 加载 API Key 静态加密密钥，必须在读写 API Key 前调用。
// 密钥材料优先级：显式外部密钥，其次 SQLCipher 主密钥派生。
// 发布前校验全部存量记录（含软删除）都是当前格式且可用目标密钥解开；
// 旧格式或无法解密的记录会让初始化失败，调用方必须中止启动并先完成离线迁移。
func (s *SystemSvc) InitSecret(ctx context.Context, explicitKey string, sqlcipherKey []byte) error {
	cipher, err := secret.NewCipher(explicitKey, sqlcipherKey)
	if err != nil {
		return err
	}
	if err := s.validateProviderSecrets(ctx, cipher); err != nil {
		return err
	}
	return secret.Activate(cipher)
}

// validateProviderSecrets 校验存量 API Key 的格式与可解密性，不重写任何数据。
func (s *SystemSvc) validateProviderSecrets(ctx context.Context, cipher *secret.Cipher) error {
	rows, err := db.EntClient.KaguyaProviderInfo.Query().
		Where(kaguyaproviderinfo.APIKeyNEQ("")).
		Select(kaguyaproviderinfo.FieldID, kaguyaproviderinfo.FieldAPIKey).
		All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if !secret.IsEncrypted(row.APIKey) {
			global.Logger.Sugar().Errorf("provider api key uses a legacy format; run cmd/migrate-provider-secrets first: id=%s", row.ID)
			return ErrProviderSecret
		}
		if _, err := cipher.Decrypt(row.APIKey); err != nil {
			global.Logger.Sugar().Errorf("cannot decrypt stored provider api key with the configured key: id=%s err=%v", row.ID, err)
			return ErrProviderSecret
		}
	}
	return nil
}
