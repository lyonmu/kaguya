package config

import (
	"context"
	"fmt"
	"log"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	gopkgviper "github.com/lyonmu/gopkg/viper"
)

func TestProviderInfo(t *testing.T) {

	// 初始化配置结构体和上下文
	var (
		cfg = Config{}
		ctx = context.Background()
	)

	// 创建配置管理器，绑定配置结构体，支持热更新
	cm := gopkgviper.NewConfigManager(&cfg)
	defer cm.Close()

	// 加载 config.yml 配置文件
	if err := cm.LoadConfig("../../config.yml", "yaml"); err != nil {
		log.Fatal(err)
	}

	// 启动后台协程监听配置变更和错误
	go func() {
		for {
			select {
			case <-cm.Watch():
				fmt.Println("config reloaded:", cm.GetConfig())
			case err := <-cm.Errors():
				log.Println("config reload failed:", err)
			}
		}
	}()

	// 根据配置创建 OpenAI 兼容协议的 provider 实例
	provider, err := openaicompat.New(
		openaicompat.WithName(cfg.Providers[0].Name),
		openaicompat.WithBaseURL(cfg.Providers[0].BaseURL),
		openaicompat.WithAPIKey(cfg.Providers[0].APIKey),
	)
	if err != nil {
		panic(err)
	}

	// 从 provider 中获取指定模型的语言模型实例
	model, err := provider.LanguageModel(ctx, cfg.Providers[0].Models[0].ID)
	if err != nil {
		panic(err)
	}

	// 创建 AI Agent，设定系统提示词
	agent := fantasy.NewAgent(
		model,
		fantasy.WithSystemPrompt("你是一个专业、简洁的中文助手。"),
	)

	// 调用 Agent 生成回答
	result, err := agent.Generate(ctx, fantasy.AgentCall{
		Prompt: "介绍一下 Kubernetes 的调度流程",
	})
	if err != nil {
		panic(err)
	}

	// 输出生成结果
	fmt.Println(result.Response.Content.Text())
}
