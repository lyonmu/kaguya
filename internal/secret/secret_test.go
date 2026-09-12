package secret

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestEncryptRoundTripWithCertificate(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatalf("init from certificate: %v", err)
	}
	if !Enabled() {
		t.Fatal("secret key should be enabled")
	}
	cipherText, err := Encrypt("sk-live-1234567890")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsEncrypted(cipherText) {
		t.Fatalf("ciphertext must carry the version prefix: %q", cipherText)
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
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
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
		if err := Init(material, ""); err != nil {
			t.Fatalf("%s key: %v", name, err)
		}
		if !Enabled() {
			t.Fatalf("%s key must enable encryption", name)
		}
	}
	Reset()
	if err := Init("too-short", ""); err == nil {
		t.Fatal("invalid key material must be rejected")
	}
}

func TestExternalKeyDoesNotDependOnCertificate(t *testing.T) {
	raw := make([]byte, keyLength)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	Reset()
	defer Reset()
	if err := Init(hex.EncodeToString(raw), testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("stable")
	if err != nil {
		t.Fatal(err)
	}
	// 轮换证书后，外部密钥仍应解开既有密文。
	if err := Init(hex.EncodeToString(raw), testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(cipherText)
	if err != nil {
		t.Fatalf("external key must survive certificate rotation: %v", err)
	}
	if plain != "stable" {
		t.Fatalf("unexpected plaintext: %q", plain)
	}
}

func TestCertificateRotationBreaksDerivedKey(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(cipherText); err == nil {
		t.Fatal("a rotated certificate must not silently decrypt old ciphertext")
	}
}

func TestUninitializedPassesThroughPlaintext(t *testing.T) {
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
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	value, err := Encrypt("")
	if err != nil || value != "" {
		t.Fatalf("empty value must stay empty: %q %v", value, err)
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

// Cipher 必须可独立构造：轮换时允许旧 Cipher 解密、新 Cipher 加密并存，
// 且构造失败不修改包级状态。
func TestCipherIndependentOfActiveState(t *testing.T) {
	Reset()
	defer Reset()
	oldCert, newCert := testPrivateKeyPEM(t), testPrivateKeyPEM(t)
	oldCipher, err := NewCipher("", oldCert)
	if err != nil {
		t.Fatal(err)
	}
	newCipher, err := NewCipher("", newCert)
	if err != nil {
		t.Fatal(err)
	}
	if !oldCipher.DerivedFromCertificate() {
		t.Fatal("certificate cipher must report derived key")
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
	// 显式密钥优先于证书派生，且不标记为证书派生。
	external, err := NewCipher(strings.Repeat("ab", 32), newCert)
	if err != nil {
		t.Fatal(err)
	}
	if external.DerivedFromCertificate() {
		t.Fatal("explicit key must not be reported as certificate-derived")
	}
}

// Init 失败不得清空已有活动密钥，成功时才会替换。
func TestInitFailureKeepsActiveKey(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	cipherText, err := Encrypt("sk-still-readable")
	if err != nil {
		t.Fatal(err)
	}
	if err := Init("not-a-valid-key", ""); err == nil {
		t.Fatal("invalid explicit key must fail")
	}
	if !Enabled() {
		t.Fatal("failed Init cleared the active key")
	}
	if plain, err := Decrypt(cipherText); err != nil || plain != "sk-still-readable" {
		t.Fatalf("active key lost after failed Init: %q %v", plain, err)
	}
}

// 凭据协调锁只用于串行化读写与轮换；活动 Cipher 的读取不进入该锁。
func TestCredentialLockSerializes(t *testing.T) {
	Reset()
	defer Reset()
	if err := Init("", testPrivateKeyPEM(t)); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		LockCredentials()
		defer UnlockCredentials()
		// 轮换持有独占锁期间仍可读取活动密钥。
		if !Enabled() {
			t.Error("active cipher must stay readable under the exclusive lock")
		}
		close(acquired)
		<-release
	}()
	<-acquired
	blocked := make(chan struct{})
	go func() {
		RLockCredentials()
		defer RUnlockCredentials()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("reader acquired the credential lock during rotation")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("reader did not acquire the credential lock after rotation")
	}
	<-done
}
