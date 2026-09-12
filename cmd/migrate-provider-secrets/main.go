// Command migrate-provider-secrets 把旧格式的提供商 API Key 离线转换为 enc:v2:。
//
// 这是一次性运维工具，不属于正常请求路径。必须先在数据库副本上执行、验证后
// 再切换到已迁移文件；SQLCipher 密钥与数据库路径都不改变。
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/secret"
	_ "github.com/mattn/go-sqlite3"
)

// 旧实现（enc:v1:）的固定参数，只在本离线迁移中复现，不进入运行时。
const (
	legacyV1Prefix    = "enc:v1:"
	legacySaltLabel   = "kaguya-secret-v1"
	legacyInfoLabel   = "provider-api-key"
	legacyKeyLength   = 32
	providerTable     = "kaguya_provider_info"
	systemInfoTable   = "kaguya_system_info"
	systemInfoIDValue = "global"
)

type options struct {
	OldSecretKey string                `name:"old-secret-key" env:"KAGUYA_OLD_SECRET_KEY" help:"Former external key material for enc:v1: records: 32 bytes as hex or base64; defaults to the TLS private key stored in the database"`
	SecretKey    string                `name:"secret-key" env:"KAGUYA_SECRET_KEY" help:"Target key material: 32 bytes as hex or base64; defaults to a key derived from the SQLCipher database key"`
	DB           config.DatabaseConfig `embed:"" prefix:"db."`
}

type stats struct {
	verified    int // 已是 enc:v2: 且可用目标密钥解开
	reencrypted int // enc:v1: 用旧密钥解出后按目标密钥重写
	encrypted   int // 历史明文加密一次
	empty       int
}

func main() {
	var opts options
	kong.Parse(&opts,
		kong.Name("migrate-provider-secrets"),
		kong.Description("Offline conversion of stored provider API keys to the enc:v2: format"),
		kong.UsageOnError(),
	)
	if err := run(context.Background(), &opts); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts *options) error {
	if err := opts.DB.ValidateSQLiteKey(); err != nil {
		return err
	}
	path, err := opts.DB.SQLitePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("open SQLCipher database: %w", err)
	}
	if err := db.RequireSQLCipher(); err != nil {
		return err
	}
	// 目标密钥优先使用显式外部密钥，否则由 SQLCipher 主密钥派生。
	sqlcipherKey, err := opts.DB.SQLCipherKeyBytes()
	if err != nil {
		return err
	}
	target, err := secret.NewCipher(opts.SecretKey, sqlcipherKey)
	if err != nil {
		return err
	}
	dsn, err := opts.DB.SQLiteDSN()
	if err != nil {
		return err
	}
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer conn.Close()
	// 单连接串行化，保证事务期间没有并发写入。
	conn.SetMaxOpenConns(1)
	result, err := migrate(ctx, conn, opts.OldSecretKey, target)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "provider secrets migrated: verified=%d reencrypted=%d encrypted=%d empty=%d\n",
		result.verified, result.reencrypted, result.encrypted, result.empty)
	return nil
}

// migrate 在单个事务内转换全部非空 API Key（含软删除行）并清空旧 TLS 字段。
// 任一行失败都会回滚，数据库保持原样。
func migrate(ctx context.Context, conn *sql.DB, oldSecretKey string, target *secret.Cipher) (stats, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return stats{}, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, "SELECT id, api_key FROM "+providerTable)
	if err != nil {
		return stats{}, fmt.Errorf("read stored provider secrets: %w", err)
	}
	defer rows.Close()
	type record struct{ id, apiKey string }
	records := make([]record, 0)
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.apiKey); err != nil {
			return stats{}, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return stats{}, err
	}
	if err := rows.Close(); err != nil {
		return stats{}, err
	}

	var legacy *legacyCipher
	var result stats
	updates := make(map[string]string, len(records))
	for _, r := range records {
		switch {
		case r.apiKey == "":
			result.empty++
			continue
		case secret.IsEncrypted(r.apiKey):
			// 目标格式必须用目标密钥校验，不能只按前缀跳过。
			if _, err := target.Decrypt(r.apiKey); err != nil {
				return stats{}, fmt.Errorf("provider %s stores an enc:v2: value that the target key cannot decrypt", r.id)
			}
			result.verified++
		case strings.HasPrefix(r.apiKey, legacyV1Prefix):
			if legacy == nil {
				legacy, err = legacyCipherFromDB(ctx, tx, oldSecretKey)
				if err != nil {
					return stats{}, err
				}
			}
			plain, err := legacy.decrypt(r.apiKey)
			if err != nil {
				return stats{}, fmt.Errorf("provider %s stores an enc:v1: value that cannot be decrypted with the provided legacy key", r.id)
			}
			rewritten, err := target.Encrypt(plain)
			if err != nil {
				return stats{}, err
			}
			updates[r.id] = rewritten
			result.reencrypted++
		case strings.HasPrefix(r.apiKey, "enc:"):
			return stats{}, fmt.Errorf("provider %s stores an unknown ciphertext version; refusing to guess", r.id)
		default:
			rewritten, err := target.Encrypt(r.apiKey)
			if err != nil {
				return stats{}, err
			}
			updates[r.id] = rewritten
			result.encrypted++
		}
	}

	for id, value := range updates {
		if _, err := tx.ExecContext(ctx, "UPDATE "+providerTable+" SET api_key = ? WHERE id = ?", value, id); err != nil {
			return stats{}, err
		}
	}
	// 旧证书私钥不再用于派生加密密钥；在提交前清空，避免遗留根密钥材料。
	// 已迁移或全新数据库可能已没有这些列，此时跳过。
	columns, err := tableColumns(ctx, tx, systemInfoTable)
	if err != nil {
		return stats{}, err
	}
	if columns["tls_certificate_pem"] && columns["tls_private_key_pem"] {
		if _, err := tx.ExecContext(ctx, "UPDATE "+systemInfoTable+" SET tls_certificate_pem = '', tls_private_key_pem = '' WHERE id = ?", systemInfoIDValue); err != nil {
			return stats{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return stats{}, err
	}
	return result, nil
}

// tableColumns 返回表的列集合；表不存在时返回空集合。
func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// legacyCipherFromDB 复现 enc:v1: 的密钥来源：优先显式外部密钥，
// 否则读取数据库中旧 TLS 私钥并按其 PKCS#8 DER 派生。
func legacyCipherFromDB(ctx context.Context, tx *sql.Tx, explicitKey string) (*legacyCipher, error) {
	if material := strings.TrimSpace(explicitKey); material != "" {
		key, err := parseLegacyExplicitKey(material)
		if err != nil {
			return nil, err
		}
		return &legacyCipher{key: key}, nil
	}
	var privateKeyPEM string
	err := tx.QueryRowContext(ctx, "SELECT tls_private_key_pem FROM "+systemInfoTable+" WHERE id = ?", systemInfoIDValue).Scan(&privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("read legacy TLS private key: %w", err)
	}
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("enc:v1: records exist but the legacy TLS private key is missing; provide --old-secret-key")
	}
	key, err := hkdf.Key(sha256.New, block.Bytes, []byte(legacySaltLabel), legacyInfoLabel, legacyKeyLength)
	if err != nil {
		return nil, fmt.Errorf("derive legacy key from TLS private key: %w", err)
	}
	return &legacyCipher{key: key}, nil
}

func parseLegacyExplicitKey(material string) ([]byte, error) {
	if decoded, err := hex.DecodeString(material); err == nil && len(decoded) == legacyKeyLength {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(material); err == nil && len(decoded) == legacyKeyLength {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("old secret key must be %d bytes encoded as hex or base64", legacyKeyLength)
}

// legacyCipher 复现旧版 AES-256-GCM + HKDF 解密，只用于读取历史密文。
type legacyCipher struct{ key []byte }

func (c *legacyCipher) decrypt(value string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, legacyV1Prefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("legacy ciphertext is truncated")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("legacy ciphertext does not match the provided key")
	}
	return string(plain), nil
}
