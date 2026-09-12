// Package secret 为提供商 API Key 等敏感配置提供静态加密。
//
// 密钥来源按优先级选择：
//
//  1. 启动参数或环境变量 KAGUYA_SECRET_KEY（32 字节，十六进制或 Base64）；
//  2. SQLCipher 主密钥经 HKDF-SHA256 域分离派生（salt=kaguya-provider-secret-v2，
//     info=provider-api-key）。
//
// 第二种来源与数据库同源，只能防止逻辑导出直接暴露 API Key，无法抵御数据库
// 文件泄露；需要独立根信任时配置外部密钥。
//
// 密文带 enc:v2: 版本前缀。enc:v1:（旧证书派生密钥）与历史明文属于旧格式，
// 由 cmd/migrate-provider-secrets 离线转换；运行时拒绝把旧格式当作明文再次加密，
// 启动初始化会校验存量记录并要求先完成迁移。
//
// 未调用 Init 时 Encrypt 原样返回明文、Decrypt 放行历史明文，便于单元测试与
// 未启用加密的调用路径；生产启动流程必然调用 Init。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// CiphertextPrefixV2 标记当前版本的密文；带版本号便于识别历史记录。
const CiphertextPrefixV2 = "enc:v2:"

const (
	keyLength  = 32 // AES-256
	saltLabel  = "kaguya-provider-secret-v2"
	infoLabel  = "provider-api-key"
	maskRunes  = 4
	maskSymbol = "••••"
)

var (
	// ErrNoKeyMaterial 表示既没有外部密钥，也没有 SQLCipher 主密钥可用于派生。
	ErrNoKeyMaterial = errors.New("no encryption key material: set KAGUYA_SECRET_KEY or provide the SQLCipher database key")
	// ErrLegacyFormat 表示取值属于旧版本格式，必须先执行离线迁移。
	ErrLegacyFormat = errors.New("value uses a legacy provider secret format; run cmd/migrate-provider-secrets before starting")
)

// Cipher 是不可变的加密对象。构造成功即持有全部密钥材料，不依赖包级状态。
type Cipher struct {
	key []byte
}

// NewKeyCipher 用 32 字节原始密钥构造 Cipher，不修改包级活动密钥。
func NewKeyCipher(key []byte) (*Cipher, error) {
	if len(key) != keyLength {
		return nil, fmt.Errorf("secret key must be %d bytes", keyLength)
	}
	copied := make([]byte, keyLength)
	copy(copied, key)
	return &Cipher{key: copied}, nil
}

// DeriveKeyFromSQLCipherKey 用固定域分离参数从 SQLCipher 主密钥派生 API Key 密钥。
// 参数是版本契约：修改会令既有 enc:v2: 密文无法解开。
func DeriveKeyFromSQLCipherKey(master []byte) ([]byte, error) {
	if len(master) == 0 {
		return nil, ErrNoKeyMaterial
	}
	derived, err := hkdf.Key(sha256.New, master, []byte(saltLabel), infoLabel, keyLength)
	if err != nil {
		return nil, fmt.Errorf("derive secret key from SQLCipher key: %w", err)
	}
	return derived, nil
}

// NewCipher 根据外部密钥或 SQLCipher 主密钥构造 Cipher，不修改包级活动密钥。
// explicitKey 为空时回退到 sqlcipherKey 派生。
func NewCipher(explicitKey string, sqlcipherKey []byte) (*Cipher, error) {
	if material := strings.TrimSpace(explicitKey); material != "" {
		parsed, err := parseExplicitKey(material)
		if err != nil {
			return nil, err
		}
		return NewKeyCipher(parsed)
	}
	derived, err := DeriveKeyFromSQLCipherKey(sqlcipherKey)
	if err != nil {
		return nil, err
	}
	return NewKeyCipher(derived)
}

// Encrypt 加密明文。空值原样返回；已是当前格式的密文不做二次加密；
// 旧格式密文明确报错，不把它当作明文再次加密。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if IsEncrypted(plain) {
		return plain, nil
	}
	if strings.HasPrefix(plain, "enc:") {
		return "", ErrLegacyFormat
	}
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return CiphertextPrefixV2 + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密密文。历史明文原样返回，保证迁移完成前仍可读；旧版本密文明确
// 报错，避免用当前密钥解出无意义结果。
func (c *Cipher) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !IsEncrypted(value) {
		if strings.HasPrefix(value, "enc:") {
			return "", ErrLegacyFormat
		}
		return value, nil
	}
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, CiphertextPrefixV2))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("ciphertext is truncated")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// 密钥材料不匹配或密文损坏；调用方应提示重新填写。
		return "", errors.New("cannot decrypt value with the current secret key")
	}
	return string(plain), nil
}

func (c *Cipher) aead() (cipher.AEAD, error) {
	if c == nil {
		return nil, errors.New("secret cipher is not initialized")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

var (
	// activeMu 只保护 active 指针本身。
	activeMu sync.RWMutex
	active   *Cipher
)

// Init 按密钥材料构造并发布活动 Cipher，必须在使用 Encrypt/Decrypt 前调用。
func Init(explicitKey string, sqlcipherKey []byte) error {
	c, err := NewCipher(explicitKey, sqlcipherKey)
	if err != nil {
		return err
	}
	return Activate(c)
}

// Activate 发布已构造好的活动 Cipher。调用方应在发布前确认存量密文可用它解开，
// 构造或校验失败时不得调用，避免用错误密钥替换可用状态。
func Activate(c *Cipher) error {
	if c == nil {
		return ErrNoKeyMaterial
	}
	activeMu.Lock()
	active = c
	activeMu.Unlock()
	return nil
}

// Reset 清除当前密钥，仅用于测试与降级路径。
func Reset() {
	activeMu.Lock()
	active = nil
	activeMu.Unlock()
}

// Enabled 报告当前是否具备可用密钥。
func Enabled() bool {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active != nil && len(active.key) > 0
}

func parseExplicitKey(material string) ([]byte, error) {
	if decoded, err := hex.DecodeString(material); err == nil && len(decoded) == keyLength {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(material); err == nil && len(decoded) == keyLength {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("secret key must be %d bytes encoded as hex or base64", keyLength)
}

// IsEncrypted 判断取值是否为当前版本密文。
func IsEncrypted(value string) bool { return strings.HasPrefix(value, CiphertextPrefixV2) }

// Encrypt 用活动密钥加密。未初始化密钥时返回明文（静态加密未启用），
// 便于未启用加密的调用路径与单元测试。
func Encrypt(plain string) (string, error) {
	activeMu.RLock()
	c := active
	activeMu.RUnlock()
	if c == nil {
		return plain, nil
	}
	return c.Encrypt(plain)
}

// Decrypt 用活动密钥解密。未初始化密钥时密文报错、历史明文原样返回。
func Decrypt(value string) (string, error) {
	activeMu.RLock()
	c := active
	activeMu.RUnlock()
	if c == nil {
		if value == "" || !strings.HasPrefix(value, "enc:") {
			return value, nil
		}
		return "", errors.New("encrypted value requires an initialized secret key")
	}
	return c.Decrypt(value)
}

// MaskUnavailable 表示已配置密钥但当前密钥无法解密（密钥材料不匹配或密文损坏）。
const MaskUnavailable = "••••••••"

// Mask 生成用于列表与详情展示的掩码，保留前四后四位便于人工辨识。
func Mask(plain string) string {
	if plain == "" {
		return ""
	}
	runes := []rune(plain)
	if len(runes) <= maskRunes*2 {
		return MaskUnavailable
	}
	return string(runes[:maskRunes]) + maskSymbol + string(runes[len(runes)-maskRunes:])
}
