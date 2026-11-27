<p align="center">
  <img src="images/kaguya.png" alt="Kaguya" width="120" />
</p>
<p align="center">用于日常对话与本地项目工作的个人桌面 AI Agent</p>
<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh.md">简体中文</a> ·
  <a href="docs/wiki.md">Wiki</a> |
  <a href="docs/wiki.zh.md">Wiki（中文）</a>
</p>

# Kaguya

**Kaguya** 是我个人使用的桌面 AI Agent：日常对话、本地项目操作、模型配置、MCP 与联网工具、历史记录和 Token 用量集中在一个应用中。后端使用 Go，React 界面随二进制一起打包，数据保存在本机的 SQLCipher 加密数据库中。

桌面安装包目前仅支持 **搭载 Apple 芯片的 macOS 14 及以上系统**。应用界面为中文。

## 功能

- **对话**：流式 Markdown 回答、Mermaid 图表、可展开的思考与工具卡片、轮次 Token / 耗时 / 工具调用统计、历史分页，以及切换页面也不会中断的并行会话；标题由持久 FIFO 队列生成，通过事件及重连快照按版本更新，默认标题可明确补生成。
- **本地项目**：在主目录内添加项目，使用内置文件与命令工具，支持文件、目录和项目根目录的分页类型化 `@` 引用、展示准备阶段及实际片段范围的引用详情，以及带文件树和统一 / 分栏 Git 差异的代码浏览器。失败轮次可在重新确认会话当前项目归属后恢复引用选择；恢复不会发送消息。
- **权限与审批**：每个会话三种模式（完全审批 / 自主审批 / 完全访问）；受限模式下 Bash、MCP 和联网工具调用均需审批，自主审批只自动放行可信只读工具，等待上限 10 分钟；拒绝后模型收到结构化错误继续本轮。审计保存关联、哈希与独立执行结果，不保存参数正文；结果无法确认时显示未知，不自动重放。
- **模型与提供商**：协议按模型配置（OpenAI Chat、OpenAI Responses、Anthropic），支持从 [models.dev](https://models.dev) 目录预填、自定义请求路径，保存前必须通过模型测试；变更模型或提供商后自动刷新模型选择列表。
- **MCP 工具**：支持 `stdio`、`streamable-http`、`sse` 服务，可动态启停，显示运行状态、工具数与失败次数，可设置单服务超时；连接断开后按指数退避自动重连，连续 5 次失败熔断并支持手动重新连接；恢复策略保存失败时停止自动重试，并单独显示存储故障。
- **联网工具**：在 **联网搜索管理** 中统一启用 DeepSeek Search 和 Fetch。支持工具调用的已配置聊天模型均可搜索网页和读取正文。
- **指令**：全局 `AGENTS.md` 动态监听并在后台加载，界面区分已保存与已发布，每轮固定同一份快照，项目指令在首个成功轮次冻结；旧合并快照可显式升级归档。
- **上下文管理**：上下文占用环、达到阈值自动压缩、未完成轮次可继续执行，失败轮次独立记录摘要准备用量。
- **用量分析**：Token 总量与峰值、最近一年活动热力图、按模型或厂商的用量构成。
- **本地优先**：SQLCipher 数据库、AES-256-GCM 加密的 API Key、Desktop 模式不监听端口。

## 安装

从 [GitHub Releases](https://github.com/lyonmu/kaguya/releases/tag/v0.1.0) 下载 `Kaguya-0.1.0.dmg`。打开 DMG，将 `Kaguya.app` 拖入「应用程序」，然后从「应用程序」打开。应用包使用 ad-hoc 签名，本版未做 Apple 公证。

升级时先退出 Kaguya，再替换应用。下次启动会打开原有本地数据库。

## 开始使用

1. 进入 **偏好与配置 → AI 配置 → 提供商与模型**，新增提供商（可从提供商目录预填名称与 Base URL）并填写 API Key；在 **模型管理** 中新增模型，先通过 **测试** 再保存。
2. 在 **系统配置** 选择默认对话模型与后台任务模型（用于生成对话标题），需要时同步目录。
3. 返回 **对话管理** 开始对话，或切换到 **项目** 添加本机目录后开展项目工作。

已安装的应用运行时不需要 Go、Bun 或独立数据库服务。模型请求使用你配置的提供商与凭据；项目命令和本地 MCP 服务使用主机上已安装的工具。网页搜索和抓取使用「联网搜索管理」中保存的 DeepSeek API Key。

## 文档

界面、限制与运行机制的完整逐页文档位于 Wiki：

| 语言 | Wiki |
| --- | --- |
| English | [docs/wiki.md](docs/wiki.md) |
| 简体中文 | [docs/wiki.zh.md](docs/wiki.zh.md) |

## 构建

源码构建需要 [go.mod](go.mod) 中的 Go 版本、Bun、Make、Git、Xcode Command Line Tools、Tcl、pkg-config、curl、Perl（macOS 上 `brew install pkgconf tcl-tk`）。请在 macOS 上为本机架构构建；C 依赖与应用的最低系统版本默认为 **14.0**。

```sh
git clone https://github.com/lyonmu/kaguya.git
cd kaguya
make package-macos      # 组装 target/Kaguya.app
make dmg-macos          # 打包 target/Kaguya-<版本>.dmg
```

| 命令 | 说明 |
| --- | --- |
| `make build` | 构建并嵌入前端，输出 `target/<仓库目录名>` |
| `make backend` / `make frontend` | 仅后端构建 / 准备嵌入资源 |
| `make native` | 下载并构建固定版本 OpenSSL + SQLCipher 静态库 |
| `make install` | 安装 CLI 到 `~/.local/bin/<仓库目录名>` |
| `make test` | `go test -race -count=1 ./...` |

签名与公证由 `CODESIGN_IDENTITY`、`NOTARY_PROFILE` 配合 `make sign-macos` / `make notarize-macos` 完成；普通打包目标只施加 ad-hoc 签名。

## 运行模式

```sh
./target/kaguya          # macOS 桌面窗口（默认）
./target/kaguya --web    # 可选 Web 模式，http://127.0.0.1:9024
```

桌面窗口通过 Wails 原生资源通道调用同一套 Go 服务，**不监听本地端口**。Web 模式共享同一界面、服务与数据；`--host`、`--port`、`--trusted-host` 仅对 Web 模式生效，远程访问应放在带认证的 HTTPS 网关后。

## 本地数据与备份

| 项目 | 位置 |
| --- | --- |
| 数据库 | `~/.kaguya/kaguya.db`（SQLCipher，WAL） |
| 数据库密钥 | `~/.kaguya/kaguya.key` |
| 覆盖项 | `--db.path` / `DB_PATH`、`--db.key-file` / `DB_KEY_FILE`、`--secret-key` / `KAGUYA_SECRET_KEY` |

备份时先退出应用，再复制数据库与剩余 WAL/SHM 文件，密钥单独妥善保存。丢失密钥即无法解密数据；不要在 Desktop 与 Web 模式下同时打开同一个数据库。API Key 使用 AES-256-GCM（`enc:v2:`），旧格式凭据需用 [cmd/migrate-provider-secrets](cmd/migrate-provider-secrets/) 离线迁移。

数据库锁等待上限为 1 秒，进程内写入由单连接串行化；出现竞争写入时返回数据库错误，避免锁等待阻塞关停 5 秒。这不代表允许多个应用实例共用数据库。

## 开发

```sh
make test                  # Go race 测试
cd web
bun install --frozen-lockfile
bun run test && bun run lint && bun run build
```

前端开发以 `./target/kaguya --web` 提供 API，再在 `web/` 中 `bun run dev`；Desktop 加载嵌入资源。修改 `internal/ent/schema/` 后执行 `go generate ./internal/ent`。协作与验证约定见 [AGENTS.md](AGENTS.md)，生成的 Swagger 文档在 [docs/](docs/)。

## 名称与许可

Kaguya 取自辉夜姬（かぐや姫），也呼应《龙族》中日本分部的 AI 系统。

[MIT](LICENSE)。
