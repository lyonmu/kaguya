package secret

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

var testMasterKey = func() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}()

// v2 派生参数是版本契约：修改 salt 或 info 会让全部既有密文无法解开。
func TestDeriveKeyFromSQLCipherKeyVector(t *testing.T) {
	derived, err := DeriveKeyFromSQLCipherKey(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	const want = "4fffbdb22d53e78d4de9ad9811ba3691f646e3d521b1fa1ff89acbb1572b30e0"
	if hex.EncodeToString(derived) != want {
		t.Fatalf("derived key = %x, want %s", derived, want)
	}
	if _, err := DeriveKeyFromSQLCipherKey(nil); !errors.Is(err, ErrNoKeyMaterial) {
		t.Fatalf("missing master key must fail, got %v", err)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatalf("init from SQLCipher key: %v", err)
	}
	if !Enabled() {
		t.Fatal("secret key should be enabled")
	}
	cipherText, err := Encrypt("sk-live-1234567890")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(cipherText, CiphertextPrefixV2) {
		t.Fatalf("ciphertext must carry the v2 prefix: %q", cipherText)
	}
	if strings.Contains(cipherText, "sk-live") {
		t.Fatalf("ciphertext must not contain the plaintext: %q", cipherText)
	}
	plain, err := Decrypt(cipherText)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "sk-live-1234567890" {
		t.Fatalf("unexpected plaintext: %q", plain)
	}
}

func TestEncryptProducesDistinctCiphertexts(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	first, err := Encrypt("same-value")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encrypt("same-value")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("a fresh nonce must produce a different ciphertext")
	}
}

func TestExplicitKeyAcceptsHexAndBase64(t *testing.T) {
	raw := make([]byte, keyLength)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	for name, material := range map[string]string{
		"hex":    hex.EncodeToString(raw),
		"base64": base64.StdEncoding.EncodeToString(raw),
	} {
		Reset()
		if err := Init(material, nil); err != nil {
			t.Fatalf("%s key: %v", name, err)
		}
		if !Enabled() {
			t.Fatalf("%s key must enable encryption", name)
		}
	}
	Reset()
	if err := Init("too-short", nil); err == nil {
		t.Fatal("invalid key material must be rejected")
	}
}

// 外部密钥优先于 SQLCipher 派生密钥，且不依赖数据库密钥。
func TestExplicitKeyOverridesDatabaseKey(t *testing.T) {
	raw := make([]byte, keyLength)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	Reset()
	defer Reset()
	if err := Init(hex.EncodeToString(raw), testMasterKey); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("stable")
	if err != nil {
		t.Fatal(err)
	}
	// 换一个数据库主密钥，外部密钥仍应解开既有密文。
	other := make([]byte, keyLength)
	if _, err := rand.Read(other); err != nil {
		t.Fatal(err)
	}
	if err := Init(hex.EncodeToString(raw), other); err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(cipherText)
	if err != nil {
		t.Fatalf("external key must survive a database key change: %v", err)
	}
	if plain != "stable" {
		t.Fatalf("unexpected plaintext: %q", plain)
	}
}

func TestSQLCipherDerivedKeyFollowsMasterKey(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("derived")
	if err != nil {
		t.Fatal(err)
	}
	other := make([]byte, keyLength)
	if _, err := rand.Read(other); err != nil {
		t.Fatal(err)
	}
	if err := Init("", other); err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(cipherText); err == nil {
		t.Fatal("a different database key must not silently decrypt old ciphertext")
	}
}

// 旧格式必须明确报错：加密端不能把 enc:v1: 字符串当作明文二次加密。
func TestLegacyFormatsRejected(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"enc:v1:AAAA", "enc:v9:AAAA"} {
		if IsEncrypted(value) {
			t.Fatalf("%q must not be treated as current ciphertext", value)
		}
		if _, err := Encrypt(value); !errors.Is(err, ErrLegacyFormat) {
			t.Fatalf("encrypting %q must fail with ErrLegacyFormat, got %v", value, err)
		}
		if _, err := Decrypt(value); !errors.Is(err, ErrLegacyFormat) {
			t.Fatalf("decrypting %q must fail with ErrLegacyFormat, got %v", value, err)
		}
	}
}

// 历史明文在迁移完成前必须保持可读，避免升级瞬间让既有提供商不可用。
func TestDecryptHistoricalPlaintext(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt("historical-plaintext")
	if err != nil || plain != "historical-plaintext" {
		t.Fatalf("plaintext = %q err=%v", plain, err)
	}
}

func TestUninitializedBehavior(t *testing.T) {
	Reset()
	value, err := Encrypt("plain")
	if err != nil {
		t.Fatalf("encrypt without key: %v", err)
	}
	if value != "plain" {
		t.Fatalf("uninitialized encryption must pass through, got %q", value)
	}
	plain, err := Decrypt("historical-plaintext")
	if err != nil {
		t.Fatalf("decrypt historical plaintext: %v", err)
	}
	if plain != "historical-plaintext" {
		t.Fatalf("unexpected value: %q", plain)
	}
	if _, err := Decrypt("enc:v1:AAAA"); err == nil {
		t.Fatal("ciphertext without a key must fail instead of returning garbage")
	}
}

func TestEmptyValueStaysEmpty(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	value, err := Encrypt("")
	if err != nil || value != "" {
		t.Fatalf("empty value must stay empty: %q %v", value, err)
	}
	plain, err := Decrypt("")
	if err != nil || plain != "" {
		t.Fatalf("empty value must decrypt to empty: %q %v", plain, err)
	}
}

func TestMask(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"short", "••••••••"},
		{"12345678", "••••••••"},
		{"sk-live-1234567890", "sk-l••••7890"},
	} {
		if got := Mask(tt.in); got != tt.want {
			t.Errorf("Mask(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Cipher 必须可独立构造：迁移与测试允许旧 Cipher 解密、新 Cipher 加密并存。
func TestCipherIndependentOfActiveState(t *testing.T) {
	Reset()
	defer Reset()
	oldKey, newKey := make([]byte, keyLength), make([]byte, keyLength)
	if _, err := rand.Read(oldKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(newKey); err != nil {
		t.Fatal(err)
	}
	oldCipher, err := NewKeyCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	newCipher, err := NewKeyCipher(newKey)
	if err != nil {
		t.Fatal(err)
	}
	cipherText, err := oldCipher.Encrypt("sk-independent")
	if err != nil {
		t.Fatal(err)
	}
	// 构造新 Cipher 不影响包级状态与旧 Cipher 的可用性。
	if Enabled() {
		t.Fatal("NewCipher must not publish the active key")
	}
	if plain, err := oldCipher.Decrypt(cipherText); err != nil || plain != "sk-independent" {
		t.Fatalf("old cipher decrypt: %q %v", plain, err)
	}
	if _, err := newCipher.Decrypt(cipherText); err == nil {
		t.Fatal("new cipher must not decrypt ciphers of the old key")
	}
	if _, err := NewKeyCipher([]byte("short")); err == nil {
		t.Fatal("wrong key length must be rejected")
	}
}

// 初始化失败不得清空已有活动密钥，成功时才会替换。
func TestActivateFailureKeepsActiveKey(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testMasterKey); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("sk-still-readable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCipher("not-a-valid-key", nil); err == nil {
		t.Fatal("invalid explicit key must fail")
	}
	if err := Activate(nil); err == nil {
		t.Fatal("activating nil must fail")
	}
	if !Enabled() {
		t.Fatal("failed initialization cleared the active key")
	}
	if plain, err := Decrypt(cipherText); err != nil || plain != "sk-still-readable" {
		t.Fatalf("active key lost after failed initialization: %q %v", plain, err)
	}
}
