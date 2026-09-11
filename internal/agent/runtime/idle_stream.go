package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"charm.land/fantasy"
)

// providerIdleTimeout 限制单次模型调用中相邻流式片段的最大间隔。
// 宿主工具（bash/edit/write）在流结束后执行，不在该看门狗覆盖范围内；
// provider 执行型工具与流并发，仍可能触发静默超时，因此取值保持宽松。
const providerIdleTimeout = 5 * time.Minute

// idleStreamModel 在模型单次 Stream 调用内检测长时间无任何片段，
// 主动取消底层请求并产生可重试的 ProviderError，避免提供商挂起时永久占用会话。
type idleStreamModel struct {
	fantasy.LanguageModel
	timeout time.Duration
}

func withIdleStreamTimeout(model fantasy.LanguageModel, timeout time.Duration) fantasy.LanguageModel {
	return idleStreamModel{LanguageModel: model, timeout: timeout}
}

func (m idleStreamModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	idleCtx, cancel := context.WithCancel(ctx)
	stream, err := m.LanguageModel.Stream(idleCtx, call)
	if err != nil {
		cancel()
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer cancel()
		timer := time.AfterFunc(m.timeout, cancel)
		defer timer.Stop()
		stream(func(part fantasy.StreamPart) bool {
			timer.Reset(m.timeout)
			return yield(part)
		})
		// 只有父 context 仍有效时才把取消归因于空闲超时；客户端取消或上层
		// 主动停止不应伪装成提供商错误。
		if idleCtx.Err() != nil && ctx.Err() == nil {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
				Title:        "provider stream idle",
				Message:      fmt.Sprintf("no stream data received for %s", m.timeout),
				StatusCode:   http.StatusServiceUnavailable,
				ResponseBody: []byte(fmt.Sprintf("no stream data received for %s", m.timeout)),
			}})
		}
	}, nil
}
