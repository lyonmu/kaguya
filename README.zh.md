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

**Kaguya** 是我个人使用的桌面 AI Agent：日常对话、本地项目操作、模型与 MCP 配置、历史记录和 Token 用量集中在一个应用中。后端使用 Go，React 界面随二进制一起打包，数据保存在本机的 SQLCipher 加密数据库中。

项目以跨平台 Desktop 应用为定位。**当前仓库已实现的原生桌面宿主与打包支持 macOS 14 及以上**；界面目前使用中文。

![Kaguya 对话界面：会话历史、流式回答与工具卡片](images/app/chat-1.png)

## 功能

- **对话**：流式 Markdown 回答、Mermaid 图表、可展开的思考与工具卡片、轮次 Token / 耗时 / 工具调用统计、历史分页，以及切换页面也不会中断的并行会话。
- **本地项目**：在主目录内的项目中工作，提供七个内置工具（`read`、`bash`、`edit`、`write`、`grep`、`find`、`ls`）、`@` 文件引用，以及带文件树和统一 / 分栏 Git 差异的代码浏览器。
- **模型与提供商**：协议按模型配置（OpenAI Chat、OpenAI Responses、Anthropic），支持从 [models.dev](https://models.dev) 目录预填、自定义请求路径，保存前必须通过模型测试。
- **MCP 工具**：支持 `stdio`、`streamable-http`、`sse` 服务，可动态启停，显示运行状态与工具数，并可设置单服务超时。
- **上下文管理**：上下文占用环、达到阈值自动压缩、未完成轮次可继续执行。
- **用量分析**：Token 总量与峰值、最近一年活动热力图、按模型或厂商的用量构成。
- **本地优先**：SQLCipher 数据库、AES-256-GCM 加密的 API Key、Desktop 模式不监听端口。

## 开始使用

1. 打开 `Kaguya.app`（使用 DMG 时先拖入 `Applications`）。
2. 进入 **偏好与配置 → AI 配置 → 提供商与模型**，新增提供商（可从提供商目录预填名称与 Base URL）并填写 API Key；在 **模型管理** 中新增模型，先通过 **测试** 再保存。
3. 在 **系统配置** 选择默认对话模型与后台任务模型（用于生成对话标题），需要时同步目录。
4. 返回 **对话管理** 开始对话，或切换到 **项目** 添加本机目录后开展项目工作。

已安装的应用运行时不需要 Go、Bun 或独立数据库服务。模型请求使用你配置的提供商与凭据；项目命令和本地 MCP 服务使用主机上已安装的工具。

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
