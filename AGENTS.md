# AGENTS.md

实验性 Go AI Agent 项目（模块 `github.com/lyonmu/kaguya`，Go 1.26）。单二进制应用：`main.go` 用 kong 解析 CLI（定义在 `internal/config/cli.go`）→ `internal/cmd.Run()` 启动 gin 服务（默认端口 9024，路由前缀 `/kaguya/api`）。无 CI、无 pre-commit、无 Go lint 配置。

## 常用命令

- `make build` → 输出 `./target/kaguya`，ldflags 把 `VERSION` 文件和 git commit/branch 注入 `github.com/lyonmu/gopkg/version`
- `make test`（即 `go test -race -count=1 ./...`）：单元测试，无需网络
- 集成测试：`go test -tags=integration ./...`，需要仓库根目录的 `config.yml`（参照 `config.example.yml`，含真实 provider API key）和网络。该文件已被 gitignore，**永远不要提交**
- 前端（`web/`）：包管理器是 **bun**（只有 `bun.lock`）。`bun run dev` / `bun run build`（`tsc -b && vite build`）/ `bun run lint`（oxlint，不是 eslint）

## 数据库（ent ORM）

- 手写 schema 只在 `internal/ent/schema/`；`internal/ent/` 下其余文件全部是生成代码，改 schema 后运行 `go generate ./internal/ent`（特性：`sql/upsert,sql/execquery,sql/modifier`），不要手改生成文件
- 生成代码和 `docs/`（swag 生成）都已提交进仓库，schema 变更后需一并提交
- 启动时自动迁移 schema（`client.Schema.Create`，外键关闭）
- DB 通过 `--db.kind sqlite|mysql` 选择，**默认是 mysql**；本地免依赖运行用 `--db.kind=sqlite`（文件在 `./data/kaguya.sqlite`，gitignored）
- schema 的 ID 由 `pkg.NewIDMixin` 生成，内部调用 `global.Id`（sonyflake）—— inserts 之前必须先初始化 `global.Id`（正常路径在 `cmd.Run()` 中完成）

## 架构要点

- 全局状态模式：`internal/global`（`Cfg`/`Logger`/`Id`/`Metrics`）和 `internal/db`（`EntClient`/`RedisCli`）是包级变量，在 `cmd.Run()` 初始化后被各处（包括 ent schema 默认值）直接引用；写测试时注意初始化顺序
- Agent 核心基于 `charm.land/fantasy` 库，分层：`internal/agent`（Service 编排）→ `runtime`（执行）→ `conversation`（历史存储）→ `tools/files`（`SafeFS`：拒绝绝对路径和逃逸 workspace root 的路径）
- 配置来源：CLI flag / 环境变量（`DB_KIND`、`MYSQL_HOST` 等，见 `internal/config/db.go` 的 struct tag）；`config.yml` **只被集成测试读取**，主程序不加载它

## 已知坑

- `internal/cmd/run.go` 里的 swag 生成注释（`swag init -g core.go -o ./internal/docs`）已过期：实际输出目录是 `./docs`，注解写在 `run.go` 上。重新生成时不要照抄该注释
- SQLite 驱动 quirks：`internal/db/db.go` 的 `init()` 把 `modernc.org/sqlite` 注册为 `"sqlite3"`，因为 ent 的 `dialect.SQLite` 用这个驱动名
- 代码注释和日志大量使用中英文混合，保持文件原有风格即可

## 工具

- 仓库根有 `.codegraph/` 索引，定位/理解代码优先用 `codegraph_explore`
