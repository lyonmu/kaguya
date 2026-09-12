// Package secret 为提供商 API Key 等敏感配置提供静态加密。
//
// 密钥来源按优先级选择：
//
//  1. 启动参数或环境变量 KAGUYA_SECRET_KEY（32 字节，十六进制或 Base64）；
//  2. TLS 证书私钥经 HKDF-SHA256 派生。
//
// 两者的安全等级不同，部署时应优先使用外部密钥：
//
//   - 外部密钥保存在数据库之外，数据库文件、备份或导出泄露时密文仍不可读；
//   - TLS 私钥与密文存放于同一个数据库，加密只提供格式混淆，无法抵御
//     数据库文件泄露。此外证书轮换会改变派生密钥，此时必须重新加密
//     （TLSUpdate 已处理），否则旧密文无法解密。
//
// 未调用 Init 时 Encrypt 原样返回明文、Decrypt 直接返回输入，便于单元测试与
// 未启用加密的调用路径。生产启动流程必然调用 Init。
//
// Cipher 是不可变对象：TLSUpdate 可以先用旧 Cipher 解密、再用新 Cipher 加密，
// 在数据库事务成功提交后才把活动密钥切换为新对象，避免轮换中途失败留下
// 新证书配旧密文的状态。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// prefix 标记密文。带版本号，便于将来更换算法时识别历史记录。
const prefix = "enc:v1:"

const (
	keyLength  = 32 // AES-256
	saltLabel  = "kaguya-secret-v1"
	infoLabel  = "provider-api-key"
	maskRunes  = 4
	maskSymbol = "••••"
)

// ErrNoKeyMaterial 表示既没有外部密钥，也没有可用于派生的证书私钥。
var ErrNoKeyMaterial = errors.New("no encryption key material: set KAGUYA_SECRET_KEY or configure a TLS certificate")

// Cipher 是不可变的加密对象。构造成功即持有全部密钥材料，
// 之后不依赖包级状态，可在轮换过程中与新 Cipher 并存。
type Cipher struct {
	key             []byte
	fromCertificate bool
}

func newCipher(key []byte, fromCertificate bool) *Cipher {
	return &Cipher{key: key, fromCertificate: fromCertificate}
}

// NewCipher 根据外部密钥或证书私钥构造 Cipher，不修改包级活动密钥。
// explicitKey 为空时回退到 tlsPrivateKeyPEM 派生。
func NewCipher(explicitKey, tlsPrivateKeyPEM string) (*Cipher, error) {
	if material := strings.TrimSpace(explicitKey); material != "" {
		parsed, err := parseExplicitKey(material)
		if err != nil {
			return nil, err
		}
		return newCipher(parsed, false), nil
	}
	if strings.TrimSpace(tlsPrivateKeyPEM) == "" {
		return nil, ErrNoKeyMaterial
	}
	derived, err := deriveFromCertificate(tlsPrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	return newCipher(derived, true), nil
}

// DerivedFromCertificate 报告该 Cipher 的密钥是否由证书私钥派生。证书轮换会改变这种密钥，
// 调用方必须先重新加密已有密文。
func (c *Cipher) DerivedFromCertificate() bool {
	return c != nil && c.fromCertificate
}

// Encrypt 加密明文。空值原样返回；已带前缀的值不做二次加密。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
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
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密密文。历史明文记录（无前缀）原样返回，便于平滑迁移。
func (c *Cipher) Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	if c == nil {
		return "", errors.New("encrypted value requires an initialized secret key")
	}
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("ciphertext is truncated")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// 证书轮换或更换外部密钥后，既有密文无法解开；调用方应提示重新填写。
		return "", errors.New("cannot decrypt value with the current secret key; re-enter the API key after a TLS certificate rotation")
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
	// activeMu 只保护 active 指针本身。读取活动 Cipher 不进入协调锁，
	// 因此 Swap 期间不会阻塞正在进行的凭据操作。
	activeMu sync.RWMutex
	active   *Cipher

	// coordMu 协调“读数据库 → 解密/加密 → 写数据库”的完整短操作。
	// 普通凭据操作共享访问，TLS 轮换独占访问。
	coordMu sync.RWMutex
)

func setActive(c *Cipher) {
	activeMu.Lock()
	active = c
	activeMu.Unlock()
}

// current 返回当前活动 Cipher；未初始化时返回 nil。
func current() *Cipher {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

// Current 返回当前活动 Cipher。调用方在使用它与数据库交互期间
// 应持有 RLockCredentials，避免与 TLS 轮换交错。
func Current() *Cipher { return current() }

// Init 加载活动加密密钥，必须在使用 Encrypt/Decrypt 前调用。
// 构造失败时不修改已有密钥，避免一次错误的显式密钥清空可用状态。
func Init(explicitKey, tlsPrivateKeyPEM string) error {
	c, err := NewCipher(explicitKey, tlsPrivateKeyPEM)
	if err != nil {
		return err
	}
	setActive(c)
	return nil
}

// Reset 清除当前密钥，仅用于测试与降级路径。
func Reset() { setActive(nil) }

// Publish 把已构造好的 Cipher 发布为活动密钥。用于 TLS 轮换：
// 事务提交后直接把候选 Cipher 切为活动状态，不再重新解析密钥材料。
func Publish(c *Cipher) { setActive(c) }

// Enabled 报告当前是否具备可用密钥。
func Enabled() bool {
	c := current()
	return c != nil && len(c.key) > 0
}

// DerivedFromCertificate 报告活动密钥是否由证书私钥派生。证书轮换会改变这种密钥，
// 调用方必须先重新加密已有密文。
func DerivedFromCertificate() bool { return current().DerivedFromCertificate() }

// LockCredentials 阻止新的凭据读写，直到 UnlockCredentials。
// TLS 轮换在“读取密文 → 用新密钥重写 → 提交事务”期间独占使用。
func LockCredentials() { coordMu.Lock() }

// UnlockCredentials 释放凭据协调锁。
func UnlockCredentials() { coordMu.Unlock() }

// RLockCredentials 声明当前操作会读取凭据密文并与轮换互斥。
func RLockCredentials() { coordMu.RLock() }

// RUnlockCredentials 释放凭据共享锁。
func RUnlockCredentials() { coordMu.RUnlock() }

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

// deriveFromCertificate 从 PKCS#8 私钥的 DER 派生对称密钥。
// 派生值只依赖私钥本身，证书链或有效期变化不会影响结果。
func deriveFromCertificate(privateKeyPEM string) ([]byte, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("TLS private key is not valid PEM")
	}
	derived, err := hkdf.Key(sha256.New, block.Bytes, []byte(saltLabel), infoLabel, keyLength)
	if err != nil {
		return nil, fmt.Errorf("derive secret key from TLS certificate: %w", err)
	}
	return derived, nil
}

// IsEncrypted 判断取值是否为本包产生的密文。
func IsEncrypted(value string) bool { return strings.HasPrefix(value, prefix) }

// Encrypt 用活动密钥加密。未初始化密钥时返回明文（静态加密未启用），
// 便于未启用加密的调用路径与单元测试。
func Encrypt(plain string) (string, error) {
	c := current()
	if c == nil {
		return plain, nil
	}
	return c.Encrypt(plain)
}

// Decrypt 用活动密钥解密。未初始化密钥时密文返回错误、历史明文原样返回。
func Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	c := current()
	if c == nil {
		return "", errors.New("encrypted value requires an initialized secret key")
	}
	return c.Decrypt(value)
}

// MaskUnavailable 表示已配置密钥但当前密钥无法解密（例如 TLS 证书已轮换）。
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
