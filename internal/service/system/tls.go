package system

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	"github.com/lyonmu/kaguya/pkg"
)

func tlsInfo(certPEM string) dto.TLSInfoResp {
	info := dto.TLSInfoResp{CertificatePEM: certPEM, Hosts: []string{}}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return info
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return info
	}
	sum := sha256.Sum256(cert.Raw)
	info.Fingerprint = hex.EncodeToString(sum[:])
	info.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	info.Hosts = append(info.Hosts, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		info.Hosts = append(info.Hosts, ip.String())
	}
	return info
}

// PrepareTLS runs after database initialization and before accepting any connections.
// Existing certificates are reused; invalid existing secrets never silently rotate.
func (s *SystemSvc) PrepareTLS(ctx context.Context, hosts []string) (*tls.Config, error) {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return nil, err
	}
	if row.TLSCertificatePem == "" && row.TLSPrivateKeyPem == "" {
		cert, key, err := pkg.SelfSignedCertificate(hosts)
		if err != nil {
			return nil, err
		}
		_, err = db.EntClient.KaguyaSystemInfo.Update().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID), kaguyasysteminfo.TLSCertificatePemEQ(""), kaguyasysteminfo.TLSPrivateKeyPemEQ("")).SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("save initial TLS configuration: %w", err)
		}
		row, err = db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
		if err != nil {
			return nil, err
		}
	}
	return pkg.ServerTLSConfig(row.TLSCertificatePem, row.TLSPrivateKeyPem)
}

// InitSecret 加载 API Key 静态加密密钥，必须在读写 API Key 前调用。
// 优先使用外部密钥（启动参数／KAGUYA_SECRET_KEY），否则从 TLS 证书私钥派生；
// 加载成功后把历史明文 API Key 就地转为密文。
func (s *SystemSvc) InitSecret(ctx context.Context, explicitKey string) error {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return err
	}
	cipher, err := secret.NewCipher(explicitKey, row.TLSPrivateKeyPem)
	if err != nil {
		return err
	}
	secret.LockCredentials()
	defer secret.UnlockCredentials()
	// 先把历史明文迁移为密文，成功后才发布活动密钥；中途失败不改变可用状态。
	if err := s.rewriteProviderSecrets(ctx, nil, cipher); err != nil {
		return err
	}
	secret.Init(explicitKey, row.TLSPrivateKeyPem)
	return nil
}

// EncryptStoredProviderSecrets 把历史明文 API Key 就地转为密文，可重复调用。
// 升级到加密存储的安装首次启动时会执行一次，之后不再有明文记录。
func (s *SystemSvc) EncryptStoredProviderSecrets(ctx context.Context) error {
	if !secret.Enabled() {
		return nil
	}
	return s.rewriteProviderSecrets(ctx, nil, secret.Current())
}

// rewriteProviderSecrets 在同一事务内重写全部已存储的 API Key。
// from 为已发布的旧 Cipher（可为 nil，表示历史明文或首次迁移）；to 为新 Cipher。
// 解密失败或任何一次写入失败时整个事务回滚，调用方不会留下混合状态。
func (s *SystemSvc) rewriteProviderSecrets(ctx context.Context, from, to *secret.Cipher) error {
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	rows, err := client.KaguyaProviderInfo.Query().
		Where(kaguyaproviderinfo.APIKeyNEQ("")).
		Select(kaguyaproviderinfo.FieldID, kaguyaproviderinfo.FieldAPIKey).All(ctx)
	if err != nil {
		return err
	}
	migrated := 0
	for _, row := range rows {
		if from == nil && secret.IsEncrypted(row.APIKey) {
			continue
		}
		if from != nil && from == to && secret.IsEncrypted(row.APIKey) {
			continue
		}
		// Decrypt 对历史明文原样返回，因此同一条路径同时覆盖首次迁移与轮换重写。
		plain, err := from.Decrypt(row.APIKey)
		if err != nil {
			global.Logger.Sugar().Errorf("cannot read provider api key before rewrite: id=%s err=%v", row.ID, err)
			return ErrProviderSecret
		}
		rewritten, err := to.Encrypt(plain)
		if err != nil {
			global.Logger.Sugar().Errorf("rewrite provider api key failed: id=%s err=%v", row.ID, err)
			return ErrProviderSecret
		}
		if err := client.KaguyaProviderInfo.UpdateOneID(row.ID).SetAPIKey(rewritten).Exec(ctx); err != nil {
			global.Logger.Sugar().Errorf("persist rewritten provider api key failed: id=%s err=%v", row.ID, err)
			return err
		}
		migrated++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if migrated > 0 {
		global.Logger.Sugar().Infof("rewrote %d provider api keys at rest", migrated)
	}
	return nil
}

func (s *SystemSvc) TLSUpdate(ctx context.Context, req *dto.TLSSaveReq) (*dto.TLSInfoResp, error) {
	cert, key := req.CertificatePEM, req.PrivateKeyPEM
	if req.Generate {
		if cert != "" || key != "" {
			return nil, ErrInvalidSystemInfo
		}
		var err error
		cert, key, err = pkg.SelfSignedCertificate(req.Hosts)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSystemInfo, err)
		}
	} else if len(req.Hosts) > 0 {
		return nil, ErrInvalidSystemInfo
	}
	if _, err := pkg.ServerTLSConfig(cert, key); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSystemInfo, err)
	}

	// 由证书私钥派生的加密密钥会随轮换失效，因此需要重新加密；
	// 使用外部密钥（KAGUYA_SECRET_KEY）时不受轮换影响。
	// 独占凭据协调锁后才根据当前活动密钥决定是否重写，并在单个事务内
	// 替换证书与全部密文；提交成功后才发布新活动密钥，中途失败不会
	// 出现新证书配旧密文。
	secret.LockCredentials()
	defer secret.UnlockCredentials()
	reencrypt := secret.Enabled() && secret.DerivedFromCertificate()
	if reencrypt {
		next, err := secret.NewCipher(global.Cfg.SecretKey, key)
		if err != nil {
			global.Logger.Sugar().Errorf("load new secret key for TLS rotation failed: %v", err)
			return nil, ErrProviderSecret
		}
		// 事务内使用轮换前后的 Cipher 对象：from 是当前活动密钥，
		// to 是尚未发布的候选密钥，事务提交后才切换活动密钥。
		if err := s.rotateProviderSecrets(ctx, secret.Current(), next, cert, key); err != nil {
			global.Logger.Sugar().Errorf("rotate provider secrets failed: %v", err)
			return nil, err
		}
		secret.Publish(next)
	} else {
		if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
			SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Exec(ctx); err != nil {
			return nil, err
		}
	}
	info := tlsInfo(cert)
	return &info, nil
}

// rotateProviderSecrets 在单个事务内保存新证书并重写全部已有密文。
// 解密失败或任意一次写入失败都会回滚，调用方保持旧证书与旧活动密钥。
func (s *SystemSvc) rotateProviderSecrets(ctx context.Context, from, to *secret.Cipher, cert, key string) error {
	if from == nil || to == nil {
		return ErrProviderSecret
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txClient := tx.Client()
	// 先确认全部密文都能用旧密钥解开，避免提交后才发现无法迁移。
	rows, err := txClient.KaguyaProviderInfo.Query().
		Where(kaguyaproviderinfo.APIKeyNEQ("")).
		Select(kaguyaproviderinfo.FieldID, kaguyaproviderinfo.FieldAPIKey).All(ctx)
	if err != nil {
		return err
	}
	rewritten := make([]string, 0, len(rows))
	for _, row := range rows {
		if !secret.IsEncrypted(row.APIKey) {
			// 历史明文：使用新密钥重新加密，保持可读。
			rewritten = append(rewritten, row.APIKey)
			continue
		}
		plain, err := from.Decrypt(row.APIKey)
		if err != nil {
			global.Logger.Sugar().Errorf("cannot read provider api key before TLS rotation: id=%s err=%v", row.ID, err)
			return ErrProviderSecret
		}
		encrypted, err := to.Encrypt(plain)
		if err != nil {
			global.Logger.Sugar().Errorf("re-encrypt provider api key after TLS rotation failed: id=%s err=%v", row.ID, err)
			return ErrProviderSecret
		}
		rewritten = append(rewritten, encrypted)
	}
	for i, row := range rows {
		if err := txClient.KaguyaProviderInfo.UpdateOneID(row.ID).SetAPIKey(rewritten[i]).Exec(ctx); err != nil {
			global.Logger.Sugar().Errorf("persist re-encrypted provider api key failed: id=%s err=%v", row.ID, err)
			return err
		}
	}
	if err := txClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Exec(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(rows) > 0 {
		global.Logger.Sugar().Infof("re-encrypted %d provider api keys after TLS rotation", len(rows))
	}
	return nil
}
