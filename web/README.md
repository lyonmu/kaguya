# Kaguya Agent Console

Kaguya 的 React 管理控制台。默认首页为对话工作区，系统管理中提供用量分析和 AI 提供商配置。

## 技术栈

- React 19 + TypeScript + Vite
- Ant Design
- Tailwind CSS 4
- Day.js

页面支持明亮/暗黑模式，并会将用户选择保存在 `localStorage` 中；首次访问默认跟随操作系统。

## 本地开发

```bash
bun install
bun run dev
```

开发服务器会将 `/kaguya/api` 代理到 `http://localhost:9024`，请先启动 Kaguya 后端。

如 API 使用其他基础地址，可在构建或启动前指定：

```bash
VITE_API_BASE_URL=https://example.com/kaguya/api bun run build
```

## 构建与嵌入

在仓库根目录执行：

```bash
make build
```

该命令会构建前端，将 `web/dist/` 同步到 Go 的嵌入资源目录，再生成包含前端静态文件的单一 Kaguya 二进制。启动后访问 `http://localhost:9024/`。

单独校验前端：

```bash
bun run test
bun run lint
bun run build
```

## 对话工作区

- 使用 `POST /v1/chat/sse` 和 Fetch 流接收回复，不使用 WebSocket；模型由后端默认配置决定，请先在「系统管理 / AI 提供商」配置可用的默认模型。
- 支持新建/续聊、标题前缀搜索、收藏、重命名、删除，以及按完整轮次向前加载历史。
- 每轮 `done` 后检查已保存标题；仍为“新对话”时调用一次 `POST /v1/chat/conversation/{id}/title/wait`。已有任务则共享等待，无任务则根据已保存首轮问答及其提供商/模型重新生成，成功后条件更新数据库，不覆盖正式或手动标题。
- 最多等待 30 秒，失败保留原标题，下一轮成功结束再尝试，不定时轮询。返回后仅更新页头和列表中的标题；切换会话、离开页面或手动改名时取消等待。
- 保留原有 GET 等待接口的只读语义。任务合并与等待通知限于当前进程，多实例部署需要会话亲和路由。
- 刷新当前会话时保留已有标题和消息；流式分配 ID 不重建消息视图，列表已有内容时不插入 loading 占位，避免闪烁和布局跳动。
- Markdown 回复、思考和工具输入输出分别呈现；Overview 展示已保存会话的累计信息，Trace 展示已加载轮次，Debug 展示公开运行字段。
- 停止生成或离开页面会中断请求，不会自动重发；失败或停止的轮次可能未持久化，可通过「更多 / 重新加载历史」确认。
- `features/chat/api.ts` 负责接口，`sse.ts` 负责流解码，`reducer.ts` 负责内容块合并，hooks 管理请求生命周期，组件负责展示。测试覆盖 SSE 分片、错误/取消和并行工具事件。

## 目录

- `src/api/`：通用 HTTP 客户端
- `src/components/`：共享布局组件
- `src/features/`：按业务领域组织的 API、类型与状态逻辑
- `src/pages/`：页面组件
- `src/app/`：应用级主题与模式配置
- `src/styles/`：Tailwind 入口及全局设计变量
