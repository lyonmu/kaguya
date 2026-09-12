package system

import (
	"context"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/secret"
)

// enableTestSecret 用固定外部密钥启用加密，测试结束后恢复未初始化状态。
func enableTestSecret(t *testing.T) {
	t.Helper()
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatalf("init secret: %v", err)
	}
	t.Cleanup(secret.Reset)
}

func providerSaveReq(name, apiKey string) *dto.SystemProviderSaveReq {
	return &dto.SystemProviderSaveReq{
		ProviderName: name, ProviderType: consts.ProviderTypeNormal,
		APIProtocol: consts.ProtocolOpenAIChat, BaseURL: "https://api.example.com/v1/chat/completions",
		APIKey: apiKey,
	}
}

func TestProviderAPIKeyIsEncryptedAtRest(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	enableTestSecret(t)
	svc := &SystemSvc{}

	created, err := svc.ProviderCreate(ctx, providerSaveReq("encrypted", "sk-live-abcdefghijkl"))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	// 响应只给掩码，不回传明文。
	if created.APIKey == "sk-live-abcdefghijkl" {
		t.Fatal("create response must not return the plaintext key")
	}
	if created.APIKey != "sk-l••••ijkl" {
		t.Fatalf("unexpected mask: %q", created.APIKey)
	}
	if !created.APIKeySet {
		t.Fatal("api_key_set must report a configured key")
	}

	// 数据库里必须是密文。
	row, err := svcProviderRow(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(row) {
		t.Fatalf("stored api key must be encrypted, got %q", row)
	}
	if strings.Contains(row, "sk-live") {
		t.Fatalf("stored api key must not contain the plaintext: %q", row)
	}

	// 显式查看接口返回明文。
	plain, err := svc.ProviderAPIKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if plain.APIKey != "sk-live-abcdefghijkl" {
		t.Fatalf("unexpected plaintext: %q", plain.APIKey)
	}

	// 列表与详情同样只返回掩码。
	detail, err := svc.ProviderDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.APIKey == "sk-live-abcdefghijkl" {
		t.Fatal("detail must not return the plaintext key")
	}
	page, err := svc.ProviderPage(ctx, &dto.SystemProviderPageReq{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.APIKey == "sk-live-abcdefghijkl" {
			t.Fatal("page must not return the plaintext key")
		}
	}
}

func TestProviderUpdateWithoutAPIKeyKeepsStoredSecret(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	enableTestSecret(t)
	svc := &SystemSvc{}

	created, err := svc.ProviderCreate(ctx, providerSaveReq("keep", "sk-original-value"))
	if err != nil {
		t.Fatal(err)
	}

	// 空 api_key 表示保留原值，避免掩码被当成新密钥写回。
	req := providerSaveReq("renamed", "")
	if _, err := svc.ProviderUpdate(ctx, created.ID, req); err != nil {
		t.Fatalf("update provider: %v", err)
	}
	plain, err := svc.ProviderAPIKey(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain.APIKey != "sk-original-value" {
		t.Fatalf("empty api_key must keep the stored secret, got %q", plain.APIKey)
	}

	// 提供新值时覆盖。
	if _, err := svc.ProviderUpdate(ctx, created.ID, providerSaveReq("renamed", "sk-replaced-value")); err != nil {
		t.Fatalf("update provider with new key: %v", err)
	}
	plain, err = svc.ProviderAPIKey(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain.APIKey != "sk-replaced-value" {
		t.Fatalf("a provided api_key must replace the stored secret, got %q", plain.APIKey)
	}
}

// svcProviderRow 读取单条提供商的原始存储值；id 为空时取唯一一条记录。
func svcProviderRow(ctx context.Context, id string) (string, error) {
	query := providerQuery(db.EntClient)
	if id != "" {
		query = query.Where(kaguyaproviderinfo.IDEQ(id))
	}
	row, err := query.Only(ctx)
	if err != nil {
		return "", err
	}
	return row.APIKey, nil
}
