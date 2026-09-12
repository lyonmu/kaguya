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
