# Kaguya 前端

Kaguya 的 React 界面由 macOS Desktop 和可选 Web 模式共用。Desktop 将前端静态文件嵌入 Go 二进制，通过 Wails 原生通道调用后端。安装方法见 [项目 README](../README.zh.md)。

## 本地开发

在仓库根目录构建并启动 Web 后端。

```sh
make build
./target/kaguya --web
```

在另一个终端启动前端开发服务器。

```sh
cd web
bun install --frozen-lockfile
bun run dev
```

Vite 将 `/kaguya/api` 代理到 `http://127.0.0.1:9024`。Web 构建可通过 `VITE_API_BASE_URL` 指定其他 API 基础地址。Desktop 使用宿主提供的 API 前缀。

## 页面与请求

- **对话管理**包含普通对话和项目工作区，支持并行会话、历史分页、工具审批与结果卡片。对话请求使用 `POST /kaguya/api/v1/chat/sse` 流式接收内容。切换页面时，运行中的轮次会继续。
- **对话标题**由后端任务生成。前端通过 `GET /kaguya/api/v1/events` 接收更新，重连时补查快照。已有会话可从菜单操作「补生成标题」。
- **偏好与配置**包含 AI 配置、系统配置和用量分析。AI 配置中的「联网搜索管理」统一设置 DeepSeek 网页搜索与抓取。两个工具可供支持工具调用的模型使用。
- 页面支持明亮与暗黑主题。首次使用跟随系统，手动选择保存在 `localStorage`。

## 构建与检查

在 `web/` 中运行测试、lint 和构建。

```sh
bun run test
bun run lint
bun run build
```

仓库根目录的 `make frontend` 将 `web/dist/` 同步到 Go 嵌入目录。用 `make build` 构建完整二进制，或用 `make dmg-macos` 构建 macOS 安装包。Desktop 的前端改动需要重新构建并启动应用。

## 源码目录

| 路径 | 用途 |
| --- | --- |
| `src/api/`、`src/platform/` | HTTP、流式请求和 Desktop 宿主适配 |
| `src/pages/`、`src/components/` | 页面与共享布局 |
| `src/features/chat/` | 对话 API、SSE 解码、状态合并、工具卡片与测试 |
| `src/features/` | 其他业务的 API、类型、状态与组件 |
| `src/app/`、`src/styles/` | 应用主题与全局样式 |

界面使用 React、TypeScript、Vite、Ant Design 和 Tailwind CSS。页面与行为细节见 [中文 Wiki](../docs/wiki.zh.md)。
