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

// InitSecret 加载 API Key 静态加密密钥，必须在读取或写入 API Key 前调用。
// 优先使用外部密钥（启动参数／KAGUYA_SECRET_KEY），否则从 TLS 证书私钥派生。
func (s *SystemSvc) InitSecret(ctx context.Context, explicitKey string) error {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return err
	}
	if err := secret.Init(explicitKey, row.TLSPrivateKeyPem); err != nil {
		return err
	}
	return s.EncryptStoredProviderSecrets(ctx)
}

// EncryptStoredProviderSecrets 将历史明文 API Key 就地转为密文，可重复调用。
// 升级到加密存储的安装首次启动时会执行一次，之后不再有明文记录。
func (s *SystemSvc) EncryptStoredProviderSecrets(ctx context.Context) error {
	if !secret.Enabled() {
		return nil
	}
	rows, err := db.EntClient.KaguyaProviderInfo.Query().
		Where(kaguyaproviderinfo.APIKeyNEQ("")).
		Select(kaguyaproviderinfo.FieldID, kaguyaproviderinfo.FieldAPIKey).All(ctx)
	if err != nil {
		return err
	}
	migrated := 0
	for _, row := range rows {
		if secret.IsEncrypted(row.APIKey) {
			continue
		}
		encrypted, err := secret.Encrypt(row.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt stored provider api key %s: %w", row.ID, err)
		}
		if err := db.EntClient.KaguyaProviderInfo.UpdateOneID(row.ID).SetAPIKey(encrypted).Exec(ctx); err != nil {
			return err
		}
		migrated++
	}
	if migrated > 0 {
		global.Logger.Sugar().Infof("encrypted %d plaintext provider api keys at rest", migrated)
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
	// 由证书私钥派生的加密密钥会随轮换失效，因此先取出明文，落库后重新加密。
	// 使用外部密钥（KAGUYA_SECRET_KEY）时不受轮换影响，无需重新加密。
	reencrypt := secret.Enabled() && secret.DerivedFromCertificate()
	pending, err := s.pendingProviderSecrets(ctx, reencrypt)
	if err != nil {
		return nil, err
	}
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Exec(ctx); err != nil {
		return nil, err
	}
	if reencrypt {
		if err := secret.Init(global.Cfg.SecretKey, key); err != nil {
			global.Logger.Sugar().Errorf("reload secret key after TLS rotation failed: %v", err)
			return nil, ErrProviderSecret
		}
		if err := s.rewrapProviderSecrets(ctx, pending); err != nil {
			return nil, err
		}
	}
	info := tlsInfo(cert)
	return &info, nil
}

// pendingProviderSecret 是轮换前后需要重新加密的单条 API Key。
type pendingProviderSecret struct {
	id    string
	plain string
}

// pendingProviderSecrets 在证书替换前用旧密钥解密所有已存储的 API Key。
// required 为 false 时直接返回，不做任何查询。
func (s *SystemSvc) pendingProviderSecrets(ctx context.Context, required bool) ([]pendingProviderSecret, error) {
	if !required {
		return nil, nil
	}
	rows, err := db.EntClient.KaguyaProviderInfo.Query().Select(kaguyaproviderinfo.FieldID, kaguyaproviderinfo.FieldAPIKey).All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query provider secrets before TLS rotation failed: %v", err)
		return nil, err
	}
	pending := make([]pendingProviderSecret, 0, len(rows))
	for _, row := range rows {
		if row.APIKey == "" {
			continue
		}
		plain, err := secret.Decrypt(row.APIKey)
		if err != nil {
			// 既有密文已不可解，轮换无法保证一致性；保留原证书而不静默丢失数据。
			global.Logger.Sugar().Errorf("cannot read provider api key before TLS rotation: id=%s err=%v", row.ID, err)
			return nil, ErrProviderSecret
		}
		pending = append(pending, pendingProviderSecret{id: row.ID, plain: plain})
	}
	return pending, nil
}

// rewrapProviderSecrets 用新派生密钥重新加密，失败时保留上一步已写入的证书。
func (s *SystemSvc) rewrapProviderSecrets(ctx context.Context, pending []pendingProviderSecret) error {
	for _, item := range pending {
		encrypted, err := secret.Encrypt(item.plain)
		if err != nil {
			global.Logger.Sugar().Errorf("re-encrypt provider api key after TLS rotation failed: id=%s err=%v", item.id, err)
			return ErrProviderSecret
		}
		if err := db.EntClient.KaguyaProviderInfo.UpdateOneID(item.id).SetAPIKey(encrypted).Exec(ctx); err != nil {
			global.Logger.Sugar().Errorf("persist re-encrypted provider api key failed: id=%s err=%v", item.id, err)
			return err
		}
	}
	if len(pending) > 0 {
		global.Logger.Sugar().Infof("re-encrypted %d provider api keys after TLS rotation", len(pending))
	}
	return nil
}
