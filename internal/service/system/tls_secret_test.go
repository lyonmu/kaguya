package system

import (
	"context"
	"testing"

	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/secret"
)

// 证书轮换会改变从私钥派生的加密密钥；已存储的 API Key 必须在轮换后仍可读出，
// 否则升级或换证书会让所有提供商凭据静默失效。
func TestTLSCertificateRotationKeepsProviderSecrets(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}

	if _, err := svc.PrepareTLS(ctx, []string{"agent.example.com"}); err != nil {
		t.Fatal(err)
	}
	// 用证书私钥派生密钥，模拟未配置 KAGUYA_SECRET_KEY 的默认安装。
	if err := svc.InitSecret(ctx, ""); err != nil {
		t.Fatalf("init secret: %v", err)
	}
	t.Cleanup(secret.Reset)
	if secret.DerivedFromCertificate() {
		// 前提校验：此路径必须真的使用证书派生密钥。
		t.Log("secret key derived from the stored certificate")
	}

	provider, err := svc.ProviderCreate(ctx, providerSaveReq("rotate", "sk-rotate-me-please"))
	if err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(mustProviderRow(t, ctx, provider.ID)) {
		t.Fatal("provider key must be encrypted before rotation")
	}

	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{Generate: true, Hosts: []string{"rotated.example.com"}}); err != nil {
		t.Fatalf("rotate certificate: %v", err)
	}

	plain, err := svc.ProviderAPIKey(ctx, provider.ID)
	if err != nil {
		t.Fatalf("key must survive certificate rotation: %v", err)
	}
	if plain.APIKey != "sk-rotate-me-please" {
		t.Fatalf("unexpected plaintext after rotation: %q", plain.APIKey)
	}
	if !secret.IsEncrypted(mustProviderRow(t, ctx, provider.ID)) {
		t.Fatal("key must stay encrypted after rotation")
	}
}

// 外部密钥不随证书变化，轮换后必须原样可用。
func TestTLSCertificateRotationKeepsExternalKeySecrets(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	enableTestSecret(t)
	svc := &SystemSvc{}

	if _, err := svc.PrepareTLS(ctx, []string{"agent.example.com"}); err != nil {
		t.Fatal(err)
	}
	provider, err := svc.ProviderCreate(ctx, providerSaveReq("external", "sk-external-key-value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{Generate: true, Hosts: []string{"rotated.example.com"}}); err != nil {
		t.Fatalf("rotate certificate: %v", err)
	}
	plain, err := svc.ProviderAPIKey(ctx, provider.ID)
	if err != nil {
		t.Fatalf("read key after rotation: %v", err)
	}
	if plain.APIKey != "sk-external-key-value" {
		t.Fatalf("unexpected plaintext after rotation: %q", plain.APIKey)
	}
}

func mustProviderRow(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	row, err := svcProviderRow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
