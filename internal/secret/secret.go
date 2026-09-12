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

var (
	mu              sync.RWMutex
	key             []byte
	fromCertificate bool
)

// Init 加载加密密钥，必须在使用 Encrypt/Decrypt 前调用。
// explicitKey 为空时回退到 tlsPrivateKeyPEM 派生。
func Init(explicitKey, tlsPrivateKeyPEM string) error {
	mu.Lock()
	defer mu.Unlock()
	key, fromCertificate = nil, false
	if material := strings.TrimSpace(explicitKey); material != "" {
		parsed, err := parseExplicitKey(material)
		if err != nil {
			return err
		}
		key = parsed
		return nil
	}
	if strings.TrimSpace(tlsPrivateKeyPEM) == "" {
		return ErrNoKeyMaterial
	}
	derived, err := deriveFromCertificate(tlsPrivateKeyPEM)
	if err != nil {
		return err
	}
	key, fromCertificate = derived, true
	return nil
}

// DerivedFromCertificate 报告当前密钥是否由证书私钥派生。证书轮换会改变这种密钥，
// 调用方必须先重新加密已有密文。
func DerivedFromCertificate() bool {
	mu.RLock()
	defer mu.RUnlock()
	return fromCertificate
}

// Reset 清除当前密钥，仅用于测试与降级路径。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	key, fromCertificate = nil, false
}

// Enabled 报告当前是否具备可用密钥。
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return len(key) > 0
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

// Encrypt 加密明文。空值原样返回；未初始化密钥时返回明文（静态加密未启用）。
func Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	aead, err := currentAEAD()
	if err != nil {
		return "", err
	}
	if aead == nil {
		return plain, nil
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密密文。历史明文记录（无前缀）原样返回，便于平滑迁移。
func Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	aead, err := currentAEAD()
	if err != nil {
		return "", err
	}
	if aead == nil {
		return "", errors.New("encrypted value requires an initialized secret key")
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

func currentAEAD() (cipher.AEAD, error) {
	mu.RLock()
	current := key
	mu.RUnlock()
	if len(current) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(current)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
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
