# AGENTS.md

## 项目与协作

- 个人使用的桌面 AI Agent，模块 `github.com/lyonmu/kaguya`；Go 版本以 `go.mod` 为准。产品以跨平台 Desktop 为定位，当前原生宿主与打包实现仅覆盖 macOS 14+，不要将其他平台桌面支持写成已实现。React 前端嵌入 Go 二进制；Desktop 不监听端口，可选 Web 模式默认监听 `127.0.0.1:9024`，API 前缀 `/kaguya/api`。
- 默认中文沟通，提交信息使用 `type(scope): 中文描述`。先理解再修改，沿用现有风格，只做任务所需变更。
- 根目录存在 `.codegraph/` 时，定位和理解代码先用 `codegraph_explore` 或 `codegraph explore`；结果不足时再精确搜索、读取源码。
- 功能与使用说明维护在 `README.md` 和 `README.zh.md`，保持双语结构、功能、命令和技术值一致；以当前实现和真实应用界面为依据，只描述已实现功能，不加入规划或未验证的平台支持。截图必须与描述匹配，不能将旧图作为当前界面证据。

## 架构与约束

- 入口：`main.go`（Kong CLI）→ `internal/cmd/`（按 `--web` 分流：默认 macOS Desktop，显式 `--web` 才启动 HTTP 服务）→ `internal/router/`、`internal/api/` → `internal/service/`。
- Desktop 宿主在 `internal/desktop/`：同一个 Gin 由 Wails 原生 scheme 直接调用，不生成本地监听端口；两张窄接口仅用于系统剪贴板与外部链接。Web 与 Desktop 共享同一初始化和关停流程，不注册业务 Binding。
- 聊天编排、历史、标题、指令快照与上下文压缩在 `internal/service/agent/`；模型执行基于 `charm.land/fantasy`，位于 `internal/agent/runtime/`；配置与用量分析在 `internal/service/system/`；项目管理、文件树和 Git 差异在 `internal/service/project/`。
- `web/src/pages/chat/` 与 `web/src/features/chat/` 承载聊天界面、并发会话状态、SSE 和内容块；项目界面在 `web/src/features/project/`。`web/src/pages/system/` 包含 AI 配置（提供商与模型、MCP 管理）、系统配置和用量分析。模型弹出层保持“提供商 → 模型”，收起显示真实模型名；API 使用本地模型记录 ID。
- `internal/agent/tools/` 提供七个工具工厂，项目聊天默认通过 `WithTools` 注册 `read/bash/edit/write/grep/find/ls`；`grep/find/ls` 依赖主机 `rg` 与 `fd`，只读且不在普通对话注册。普通对话不注册内置文件/命令工具，但普通与项目对话都可使用已启用的 MCP 工具；标题任务不使用工具。续聊目录必须来自数据库项目归属，不接受请求覆盖。
- 项目路径限制在运行用户主目录内，规范化后唯一；删除项目只解除会话归属，不删除历史或主机文件。项目 `@` 文件引用、内容预览和 Git 差异必须复用服务端路径校验、忽略规则和大小限制，不信任前端路径。
- MCP 运行时位于 `internal/agent/mcp/`，配置与动态启停在 `internal/service/system/mcp.go`。支持 `stdio/streamable-http/sse`；新配置默认停用，启用时发现工具，修改启用服务先验证替代连接。停用/删除需关闭连接并取消请求；HTTP 请求保持配置 URL 同源，拒绝跨源重定向与 HTTPS 降级。列表不返回环境变量和认证头，编辑详情含原值，禁止写入日志。
- 文件工具用 `os.Root` 限制工作区；bash 以服务进程权限运行，工作目录不是沙箱。工具副作用立即生效，不随对话取消或数据库回滚而撤销。保持取消/超时终止进程组、输出截断和同文件修改串行；日志不要记录原始命令或文件内容。
- 超长 Bash 输出使用 `global.Id.GenID()` 生成文件 ID，保存到 `/tmp/kaguya/YYYYMMDD/<conversation-id>/bash-<id>.log`；仅当前会话可通过 `read` 读取其输出，保持输出和目录配额。生命周期交给 OS 临时目录机制，不在历史分页或应用关闭时删除。
- 全局指令与项目根目录 `AGENTS.md` 在会话首次成功轮次保存快照，后续跨重启和压缩复用；空快照与尚未初始化必须区分。全局路径按设置顺序读取，文件名大小写不敏感，合计上限 256 KiB，项目指令通过 `os.Root` 读取。修改指令文件/路径影响新会话，不应中途重读覆盖快照。
- 上下文占用来自最近模型调用，不能用累计消费替代；自动压缩保留原始历史和工具调用/结果配对，成功轮次事务保存续聊快照。摘要用当前聊天模型且不带工具，摘要用量计入本轮；窗口未知时跳过压缩，失败明确报错。聊天重试由 `chat_max_retries` 控制（默认 5，范围 0–20），已输出内容后不重试。
- 启动参数来自 CLI／环境变量，主程序不加载 `config.yml`；提供商、模型和系统提示词保存在数据库中。API Key 在写库前用 `internal/secret` 加密（`enc:v2:`）：密钥优先取 `KAGUYA_SECRET_KEY`，否则由 SQLCipher 主密钥经 HKDF 派生。启动会校验存量记录，旧格式必须先用 `cmd/migrate-provider-secrets` 离线转换；不要恢复运行时就地重写或证书派生密钥。
- `internal/global` 和 `internal/db` 使用包级状态。测试沿用已有初始化与清理模式，插入依赖生成 ID 的记录前必须初始化 `global.Id`，避免并行测试污染共享状态。
- 生成前写入 `running` 占位轮次并节流增量落库；只有 `completed` 轮次进入完整历史与用量，事务提交后才能发送 `done`。断联/超时标 `interrupted`、用户主动停止标 `canceled`、生成错误标 `failed`，都保留已推送内容供展示；未完成轮次的用户提问会按轮次顺序拼回下一轮（对齐 pi：用户消息始终保留，不完整的助手消息过滤），半截助手内容与工具记录不进入上下文。SSE 连接即轮次生命周期，切换会话不关闭连接、轮次继续执行。标题任务独立于聊天上下文与聊天用量。

## 数据与部署

- 当前应用按 **macOS Desktop 默认 + 可选 HTTP 服务 + SQLCipher 单后端** 使用：macOS 无参数启动为原生窗口，`--web` 启动 HTTP 服务（`--host`／`--port`／`--trusted-host` 仅对 `--web` 生效）。`make install` 完整构建并安装二进制到 `~/.local/bin/<仓库目录名>`，须先创建目标目录；`make package-macos` 组装 `target/Kaguya.app`，`make dmg-macos` 打包 `target/Kaguya-<版本>.dmg`。主机须提供 Bash 和项目工具链，默认不构建或测试 Docker。
- 默认数据与密钥在 `~/.kaguya/kaguya.db` / `~/.kaguya/kaguya.key`，不写入应用包。仅全新数据库且未指定密钥路径时自动生成默认密钥；已有数据缺密钥必须报错。备份须退出应用并保留数据库、剩余 WAL/SHM 和密钥，不同时以多个实例打开同一数据库。
- Finder 启动时 `internal/desktop/environment_darwin.go` 尝试从登录 shell 补充 `PATH`（5 秒超时），只导入 PATH，不导入代理等其他环境变量；终端启动沿用已有环境。不要把主机目录或终端环境硬编码进应用。
- `make native` 先在 `target/` 下构建固定版本的 C 依赖：macOS 构建 OpenSSL 静态库到 `target/openssl`，再构建 SQLCipher 到 `target/sqlcipher`；两者都以 `MACOSX_DEPLOYMENT_TARGET`（默认 14.0）为部署目标。Go 侧固定使用 `github.com/mattn/go-sqlite3`（`USE_LIBSQLITE3`），`modernc.org/sqlite` 仅作为测试用的明文对照引擎。不要重新引入其他数据库驱动。
- Web 模式的 `--web` 提供入站 HTTP，公开访问的 TLS 由网关负责；聊天传输为 POST SSE，不恢复应用 WebSocket。网关需传递原始外部 Host／Origin／Sec-Fetch-Site，并通过 `--trusted-host` 精确放行。Desktop 不创建监听端口，请求来源由原生通道中间件校验。

## 构建与验证

| 场景 | 命令 |
| --- | --- |
| Go 测试 | `make test`（`go test -race -count=1 ./...`） |
| 前端依赖 | 在 `web/` 执行 `bun install --frozen-lockfile`，不引入其他包管理器锁文件 |
| 前端验证 | 在 `web/` 执行 `bun run test`、`bun run lint`（oxlint）、`bun run build`（TypeScript + Vite） |
| 完整构建 | `make build`：构建并嵌入前端，再输出 `target/<仓库目录名>` |
| 仅后端构建 | `make backend`，需先由 `make frontend` 准备嵌入资源；使用 `production` 构建标签 |
| macOS 应用包 | `make package-macos`（组装 `target/Kaguya.app` 并核对 C 静态库的最低系统版本）、`make dmg-macos`（打包拖拽安装的 `target/Kaguya-<版本>.dmg`）；签名／公证用 `CODESIGN_IDENTITY`、`NOTARY_PROFILE` 驱动 `make sign-macos`／`make notarize-macos`（后者同时公证并 staple 应用包与 DMG） |
| 前端开发 | 在 `web/` 执行 `bun run dev`；先以 `./target/kaguya --web` 保持 API 于 `http://127.0.0.1:9024`。Desktop 不用 Vite dev server |
| 旧凭据迁移 | `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh build -o ./target/migrate-provider-secrets ./cmd/migrate-provider-secrets`，只在数据库副本上执行 |

行为变更补充相应测试，运行受影响部分的验证；失败先定位原因，不削弱测试。纯文档改动检查链接、命令与 `git diff --check`。完成后说明变更、验证结果及未验证项。

## 生成文件

- Ent schema 只在 `internal/ent/schema/` 修改，随后运行 `go generate ./internal/ent`；不要手改生成代码，保留 `generate.go` 中的生成特性。启动自动迁移时使用 `migrate.WithForeignKeys(false)` 不生成外键约束；连接本身的 `_foreign_keys=on` 保持启用，不要混淆两者。
- API 注解变化后同步 `docs/`，生成命令以 `internal/cmd/run.go` 顶部注释为准。Ent 与 Swagger 的受版本控制产物随对应源码更新。
- 不提交凭据、运行数据或构建产物。工具生成的报告、截图等非运行时资料默认不提交；修改、删除或暂存已有此类资料前需有用户授权。
