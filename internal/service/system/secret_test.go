package system

import (
	"errors"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/secret"
)

func testSQLCipherKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// InitSecret 是启动时的唯一密钥发布入口：同一密钥重复初始化必须通过，
// 不同密钥或旧格式记录必须失败，且失败不得清空已经可用的活动密钥。
func TestInitSecretValidatesStoredCiphertext(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	key := testSQLCipherKey(1)
	if err := svc.InitSecret(ctx, "", key); err != nil {
		t.Fatalf("init secret: %v", err)
	}
	t.Cleanup(secret.Reset)

	provider, err := svc.ProviderCreate(ctx, providerSaveReq("validated", "sk-validated-value"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSecret(ctx, "", key); err != nil {
		t.Fatalf("re-init with the same key must pass: %v", err)
	}
	if err := svc.InitSecret(ctx, "", testSQLCipherKey(9)); err == nil {
		t.Fatal("init with a different key must fail")
	}
	// 失败后仍用原活动密钥读取，说明可用状态未被清空。
	plain, err := svc.ProviderAPIKey(ctx, provider.ID)
	if err != nil || plain.APIKey != "sk-validated-value" {
		t.Fatalf("active cipher lost after failed init: %+v err=%v", plain, err)
	}
}

// 非法显式密钥在构造阶段失败，不得替换活动密钥。
func TestInitSecretKeepsActiveCipherOnInvalidExplicitKey(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	if err := svc.InitSecret(ctx, "", testSQLCipherKey(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	provider, err := svc.ProviderCreate(ctx, providerSaveReq("keep", "sk-keep-me-please"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSecret(ctx, "not-a-valid-key", testSQLCipherKey(1)); err == nil {
		t.Fatal("invalid explicit key must fail")
	}
	if !secret.Enabled() {
		t.Fatal("failed init must keep the previous active key")
	}
	plain, err := svc.ProviderAPIKey(ctx, provider.ID)
	if err != nil || plain.APIKey != "sk-keep-me-please" {
		t.Fatalf("key lost after failed init: %+v err=%v", plain, err)
	}
}

// 旧格式记录必须中止启动并提示先做离线迁移，运行时不会就地重写。
func TestInitSecretRejectsLegacyRecords(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	key := testSQLCipherKey(1)
	if err := svc.InitSecret(ctx, "", key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	for name, value := range map[string]string{
		"legacy v1":  "enc:v1:AAAA",
		"plaintext":  "sk-historical-plaintext",
		"unknown v9": "enc:v9:AAAA",
	} {
		row, err := db.EntClient.KaguyaProviderInfo.Create().
			SetProviderName(name).SetAPIProtocol(consts.ProtocolOpenAIChat).
			SetAPIKey(value).SetBaseURL("https://api.example.com/v1").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.InitSecret(ctx, "", key); !errors.Is(err, ErrProviderSecret) {
			t.Fatalf("%s: expected ErrProviderSecret, got %v", name, err)
		}
		if stored, err := svcProviderRow(ctx, row.ID); err != nil || stored != value {
			t.Fatalf("%s: failed init rewrote the stored value: %q err=%v", name, stored, err)
		}
	}
}
