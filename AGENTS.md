# AGENTS.md

## 项目与协作

- 实验性 Go AI Agent，模块 `github.com/lyonmu/kaguya`；Go 版本以 `go.mod` 为准。React 前端嵌入 Go 二进制，默认端口 `9024`，API 前缀 `/kaguya/api`。
- 默认中文沟通，提交信息使用 `type(scope): 中文描述`。先理解再修改，沿用现有风格，只做任务所需变更。
- 根目录存在 `.codegraph/` 时，定位和理解代码先用 `codegraph_explore` 或 `codegraph explore`；结果不足时再精确搜索、读取源码。
- 功能与部署说明维护在 `README.md` 和 `README.zh.md`，修改时保持双语内容一致；知识库、长期记忆与向量检索仍属后续规划。

## 架构与约束

- 入口：`main.go`（Kong CLI）→ `internal/cmd/`（初始化）→ `internal/router/`、`internal/api/` → `internal/service/`。
- 聊天编排、历史与标题生成在 `internal/service/agent/`；模型执行基于 `charm.land/fantasy`，位于 `internal/agent/runtime/`；配置与用量分析在 `internal/service/system/`。
- `internal/agent/files/` 提供 workspace 范围内的只读工具，默认聊天链路尚未挂载；不要绕过路径限制。
- 启动参数来自 CLI／环境变量，主程序不加载 `config.yml`；提供商、模型和系统提示词保存在数据库中。
- `internal/global` 和 `internal/db` 使用包级状态。测试沿用已有初始化与清理模式，插入依赖生成 ID 的记录前必须初始化 `global.Id`，避免并行测试污染共享状态。
- 只有成功完成的聊天轮次才持久化，事务提交后才能发送 `done`。失败或取消的部分回答不进入完整历史；标题任务独立于聊天上下文与聊天用量。

## 数据与部署

- 统一部署方向为 **PostgreSQL + pgvector、Docker + Docker Compose**。不要新增其他数据库部署方案，也不要因文档调整擅自移除遗留驱动或测试。
- 从仓库根目录执行 `docker build -t kaguya:latest .`，再执行 `docker compose up -d`；Compose 使用本地镜像，不会自动构建。
- 当前 Compose 使用 host 网络，应用连接 `127.0.0.1:5432`，数据挂载在 `./pgvector`。调整容器参数时保留 `--db.kind=postgresql` 并同步数据库凭据；不要删除已有数据目录。
- Docker 运行层需要 CA 证书及健康检查使用的 shell／`pidof`，修改镜像时保留这些依赖。

## 构建与验证

| 场景 | 命令 |
| --- | --- |
| Go 测试 | `make test`（`go test -race -count=1 ./...`） |
| 前端依赖 | 在 `web/` 执行 `bun install --frozen-lockfile`，不引入其他包管理器锁文件 |
| 前端验证 | 在 `web/` 执行 `bun run test`、`bun run lint`（oxlint）、`bun run build`（TypeScript + Vite） |
| 完整构建 | `make build`：构建并嵌入前端，再输出 `target/<仓库目录名>` |
| 仅后端构建 | `make backend`，需先由 `make frontend` 准备嵌入资源 |
| 前端开发 | 在 `web/` 执行 `bun run dev`，API 默认代理到 `http://localhost:9024` |

行为变更补充相应测试，运行受影响部分的验证；失败先定位原因，不削弱测试。纯文档改动检查链接、命令与 `git diff --check`。完成后说明变更、验证结果及未验证项。

## 生成文件

- Ent schema 只在 `internal/ent/schema/` 修改，随后运行 `go generate ./internal/ent`；不要手改生成代码，保留 `generate.go` 中的生成特性。启动时自动迁移 schema，外键约束关闭。
- API 注解变化后同步 `docs/`，生成命令以 `internal/cmd/run.go` 顶部注释为准。Ent 与 Swagger 的受版本控制产物随对应源码更新。
- 不提交凭据、运行数据或构建产物。工具生成的报告、截图等非运行时资料默认不提交；修改、删除或暂存已有此类资料前需有用户授权。
