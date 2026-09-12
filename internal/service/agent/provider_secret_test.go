package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/secret"
)

// seedProvider 写入一条提供商记录。加密发生在服务层，所以这里显式加密，
// 以复现经 ProviderCreate 保存后的真实存储状态。
func seedProvider(t *testing.T, ctx context.Context, client *ent.Client, name, apiKey string) *ent.KaguyaProviderInfo {
	t.Helper()
	encrypted, err := secret.Encrypt(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaProviderInfo.Create().
		SetProviderName(name).SetAPIProtocol(consts.ProtocolOpenAIChat).
		SetAPIKey(encrypted).SetBaseURL("https://api.example.com/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// rotateSecretKey 换用另一个密钥，使既有密文无法解开，模拟证书被外部替换
// 而数据库未同步。
func rotateSecretKey(t *testing.T) {
	t.Helper()
	if err := secret.Init(strings.Repeat("cd", 32), nil); err != nil {
		t.Fatal(err)
	}
}

// chatFrames 汇总一次对话产生的关键帧。
type chatFrames struct {
	done int
	err  error
}

// runChat 同步消费一次对话的全部帧。
func runChat(ctx context.Context, t *testing.T, req *dtochat.ChatReq) chatFrames {
	t.Helper()
	var frames chatFrames
	ch := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, ch, req)
	for frame := range ch {
		if frame.Err != nil {
			frames.err = frame.Err
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			frames.done++
		}
	}
	return frames
}

// 证书被外部替换后，已存储的密文无法用当前密钥解开。此时必须返回可识别的哨兵，
// 让 API 层提示重新填写，而不是笼统的“对话生成失败”。
func TestChatReportsUndecryptableProviderSecret(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	provider := seedProvider(t, ctx, client, "rotated", "sk-stale-value")
	stored, err := client.KaguyaProviderInfo.Query().Where(kaguyaproviderinfo.IDEQ(provider.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(stored.APIKey) {
		t.Fatalf("precondition: stored key must be ciphertext, got %q", stored.APIKey)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("rotated").SetModelID("rotated").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	rotateSecretKey(t)

	frames := runChat(ctx, t, &dtochat.ChatReq{Messages: "解密失败不应落库"})
	if frames.done != 0 {
		t.Fatal("an undecryptable secret must not report a completed turn")
	}
	if !errors.Is(frames.err, ErrProviderSecretUnavailable) {
		t.Fatalf("expected ErrProviderSecretUnavailable, got %v", frames.err)
	}
	if n, err := client.KaguyaChatTurn.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("failed turn must not persist: turns=%d err=%v", n, err)
	}
}

// 标题任务与对话共用同一解密路径，也必须返回该哨兵而不是“任务模型未配置”：
// 模型其实已经配置，失败原因是凭据不可用。
func TestTitleGenerationReportsUndecryptableProviderSecret(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	provider := seedProvider(t, ctx, client, "rotated", "sk-stale-value")
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("rotated").SetModelID("rotated").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTaskModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// saveCompletedTurn 会以默认标题创建会话，满足标题生成的前置条件。
	if err := saveCompletedTurn(ctx, testCompletedTurn("900", 0)); err != nil {
		t.Fatal(err)
	}
	rotateSecretKey(t)

	_, err = (&AgentSvc{}).ConversationTitleGenerate(ctx, "900")
	if !errors.Is(err, ErrProviderSecretUnavailable) {
		t.Fatalf("expected ErrProviderSecretUnavailable, got %v", err)
	}
}

// 该哨兵只覆盖“密文解不开”：未配置密钥与历史明文记录都不应被误判。
func TestProviderSecretSentinelIsNotOverBroad(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	for name, apiKey := range map[string]string{"empty": "", "plaintext": "sk-legacy-plaintext"} {
		row, err := client.KaguyaProviderInfo.Create().
			SetProviderName(name).SetAPIProtocol(consts.ProtocolOpenAIChat).
			SetAPIKey(apiKey).SetBaseURL("https://api.example.com/v1/chat/completions").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := providerAPIKey(row, "1")
		if err != nil {
			t.Fatalf("%s: stored key must decrypt, got %v", name, err)
		}
		if plain != apiKey {
			t.Fatalf("%s: plaintext = %q, want %q", name, plain, apiKey)
		}
	}
}

// providerAPIKey 是两条解密路径的共同入口，单独验证它返回哨兵。
func TestProviderAPIKeyReturnsSentinelOnFailure(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	provider := seedProvider(t, ctx, client, "rotated", "sk-stale-value")
	rotateSecretKey(t)

	if _, err := providerAPIKey(provider, "42"); !errors.Is(err, ErrProviderSecretUnavailable) {
		t.Fatalf("expected ErrProviderSecretUnavailable, got %v", err)
	}
}
