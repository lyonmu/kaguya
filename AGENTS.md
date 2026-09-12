# AGENTS.md

## 项目与协作

- 实验性 Go AI Agent，模块 `github.com/lyonmu/kaguya`；Go 版本以 `go.mod` 为准。React 前端嵌入 Go 二进制，默认端口 `9024`，API 前缀 `/kaguya/api`。
- 默认中文沟通，提交信息使用 `type(scope): 中文描述`。先理解再修改，沿用现有风格，只做任务所需变更。
- 根目录存在 `.codegraph/` 时，定位和理解代码先用 `codegraph_explore` 或 `codegraph explore`；结果不足时再精确搜索、读取源码。
- 功能与部署说明维护在 `README.md` 和 `README.zh.md`，修改时保持双语内容一致；知识库、长期记忆与向量检索仍属后续规划。

## 架构与约束

- 入口：`main.go`（Kong CLI）→ `internal/cmd/`（初始化）→ `internal/router/`、`internal/api/` → `internal/service/`。
- 聊天编排、历史与标题生成在 `internal/service/agent/`；模型执行基于 `charm.land/fantasy`，位于 `internal/agent/runtime/`；配置与用量分析在 `internal/service/system/`。
- `internal/agent/tools/` 提供 pi 风格的七个工具（无 PowerShell），项目聊天默认通过 `WithTools` 注册 `read/bash/edit/write`；普通对话与标题任务不注册主机工具。续聊目录必须来自数据库项目归属，不接受请求覆盖。
- 文件工具用 `os.Root` 限制工作区；bash 以服务进程权限运行，工作目录不是沙箱。工具副作用立即生效，不随对话取消或数据库回滚而撤销。保持取消/超时终止进程组、输出截断和同文件修改串行；日志不要记录原始命令或文件内容。
- 启动参数来自 CLI／环境变量，主程序不加载 `config.yml`；提供商、模型和系统提示词保存在数据库中。API Key 在写库前用 `internal/secret` 加密（`enc:v2:`）：密钥优先取 `KAGUYA_SECRET_KEY`，否则由 SQLCipher 主密钥经 HKDF 派生。启动会校验存量记录，旧格式必须先用 `cmd/migrate-provider-secrets` 离线转换；不要恢复运行时就地重写或证书派生密钥。
- `internal/global` 和 `internal/db` 使用包级状态。测试沿用已有初始化与清理模式，插入依赖生成 ID 的记录前必须初始化 `global.Id`，避免并行测试污染共享状态。
- 生成前写入 `running` 占位轮次并节流增量落库；只有 `completed` 轮次进入完整历史与用量，事务提交后才能发送 `done`。断联/超时标 `interrupted`、用户主动停止标 `canceled`、生成错误标 `failed`，都保留已推送内容供展示；未完成轮次的用户提问会按轮次顺序拼回下一轮（对齐 pi：用户消息始终保留，不完整的助手消息过滤），半截助手内容与工具记录不进入上下文。SSE 连接即轮次生命周期，切换会话不关闭连接、轮次继续执行。标题任务独立于聊天上下文与聊天用量。

## 数据与部署

- 当前应用按 **主机原生服务 + SQLCipher 单后端** 使用，`make install` 完整构建并安装二进制到 `~/.local/bin/<仓库目录名>`。数据库是唯一运行时存储，网络数据库（MySQL/PostgreSQL）与 Redis 代码已移除；主机须提供 Bash 和项目工具链，默认不构建或测试 Docker。
- `make native` 先把 SQLCipher/OpenSSL 静态库构建到 `target/sqlcipher`；Go 侧固定使用 `github.com/mattn/go-sqlite3`（`USE_LIBSQLITE3`）与 `modernc.org/sqlite` 仅作为测试用的明文对照引擎。不要重新引入其他数据库驱动。
- 应用只提供入站 HTTP，公开访问的 TLS 由网关负责；聊天传输为 POST SSE，不恢复应用 WebSocket。网关需传递原始外部 Host／Origin／Sec-Fetch-Site，并通过 `--trusted-host` 精确放行。
- 以下 Docker / Compose 配置是保留的容器部署描述，本机 Web 目标下未实际验证：从仓库根目录执行 `docker build -t kaguya:latest .`，准备 `./kaguya-key`（64 位十六进制、`chmod 600`）后执行 `docker compose up -d`；Compose 使用本地镜像，不会自动构建。容器只发布到宿主 `127.0.0.1:9024`，数据在 `./kaguya-data`。不要删除已有数据目录。
- Docker 运行层需要 Bash（Agent 工具）与 CA 证书；健康检查依赖 `pidof`。构建层需要 SQLCipher 所需的静态 OpenSSL 与 pkg-config；修改镜像时保留这些依赖。

## 构建与验证

| 场景 | 命令 |
| --- | --- |
| Go 测试 | `make test`（`go test -race -count=1 ./...`） |
| 前端依赖 | 在 `web/` 执行 `bun install --frozen-lockfile`，不引入其他包管理器锁文件 |
| 前端验证 | 在 `web/` 执行 `bun run test`、`bun run lint`（oxlint）、`bun run build`（TypeScript + Vite） |
| 完整构建 | `make build`：构建并嵌入前端，再输出 `target/<仓库目录名>` |
| 仅后端构建 | `make backend`，需先由 `make frontend` 准备嵌入资源 |
| 前端开发 | 在 `web/` 执行 `bun run dev`，API 默认代理到 `http://127.0.0.1:9024` |
| 旧凭据迁移 | `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh build -o ./target/migrate-provider-secrets ./cmd/migrate-provider-secrets`，只在数据库副本上执行 |

行为变更补充相应测试，运行受影响部分的验证；失败先定位原因，不削弱测试。纯文档改动检查链接、命令与 `git diff --check`。完成后说明变更、验证结果及未验证项。

## 生成文件

- Ent schema 只在 `internal/ent/schema/` 修改，随后运行 `go generate ./internal/ent`；不要手改生成代码，保留 `generate.go` 中的生成特性。启动时自动迁移 schema，外键约束关闭。
- API 注解变化后同步 `docs/`，生成命令以 `internal/cmd/run.go` 顶部注释为准。Ent 与 Swagger 的受版本控制产物随对应源码更新。
- 不提交凭据、运行数据或构建产物。工具生成的报告、截图等非运行时资料默认不提交；修改、删除或暂存已有此类资料前需有用户授权。
