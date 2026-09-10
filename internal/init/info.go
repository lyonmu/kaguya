package initialize

import (
	"context"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
)

// Info 原子地创建缺失的系统配置，多实例同时启动也不会覆盖已有值。
// system_prompt 是追加提示词，初始设置默认中文交流；基础人设由聊天请求另行拼接。
// 不初始化模型选择，也不读取旧模型标记。
func Info(ctx context.Context, client *ent.Client) error {
	return client.KaguyaSystemInfo.Create().
		SetID(consts.SystemInfoID).
		SetUserAgent(consts.DefaultUserAgent).
		SetSystemPrompt(`## Language

* Communicate with the user in Chinese by default.`).
		OnConflictColumns(kaguyasysteminfo.FieldID).
		Ignore().
		Exec(ctx)
}
