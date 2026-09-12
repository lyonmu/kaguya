# Kaguya 优化方案

> 分析日期：2026-09-12。基线提交：`add6997fd10e1421d2c0afaaac4569e7f077c053`。本次只生成本文件，不实施代码、配置、迁移或部署变更。
>
> 结论来自当前源码、调用链、现有测试、静态检查和隔离实验。下文区分“已验证缺陷”“静态确认的故障路径”“有条件风险”和“需要测量收益的性能改进”；不将测试通过等同于不存在逻辑竞态，也不把推测写成已发生的生产事故。

## 1. 项目现状与分析边界

### 1.1 当前实现与既有约定的差异

当前项目是单用户、单实例的 Go Agent 服务，前端为 React + TypeScript。应保留此定位，不以本次优化为由迁移数据库、引入多租户、微服务、消息队列或重写 Agent 框架。

必须先明确三处实际差异，避免按旧说明设计错误方案：

- `internal/cmd/run.go:78` 的 PostgreSQL/MySQL 启动分支已注释，`internal/config/db.go:26` 默认 `sqlite`，当前有效运行路径是 **SQLCipher + WAL + 单数据库连接**。仓库协作说明仍提及 PostgreSQL/pgvector。本方案分析当前有效代码，不恢复或删除遗留数据库分支；未来是否恢复须另行决定。
- `Makefile` 当前 `install` 目标安装到 `~/.local/bin/$(PROJECT_NAME)`，不是旧约定中的 `/usr/bin`。`make test` 先构建 SQLCipher，再经 `scripts/go-sqlcipher.sh` 执行 race 测试，不能用默认 CGO 环境下的裸 `go test` 替代。
- 服务默认 `https://127.0.0.1:9024`、TLS 1.3；前端开发代理是 `https://localhost:9024` 并验证本地 CA。上下文压缩、全局/项目指令快照和 MCP 已实现；普通对话不注册内置主机工具，但仍注入全局启用的 MCP 工具。

这些差异仅记录于本方案，本次不修改 `AGENTS.md`、README、Makefile 或配置。

### 1.2 架构和关键调用链

| 层次 | 当前职责与主要文件 | 应保持的边界 |
| --- | --- | --- |
| 启动 | `main.go` → `internal/cmd/run.go`：Kong 参数、日志、ID、SQLCipher、迁移、TLS、凭据、MCP、HTTP | 在启动处组装生命周期；不让 service 自行启动第二套服务 |
| HTTP | `pkg/gin.go`、`pkg/request_security.go`、`internal/router/`、`internal/api/v1/` | Host/Origin 检查、1 MiB 请求限制、绑定参数、业务错误映射、SSE/WS 编码 |
| 聊天编排 | `internal/service/agent/chat.go`、`chat_execute.go`、`chat_prompt.go` | 模型选择、会话租约、16 个聊天槽位、工作区与指令、流式生成及终态 |
| 历史与记录 | `conversation.go`、`chat_recorder.go`、`chat_trace.go`、`compaction.go` | 展示块和模型上下文分离；增量保存；完成事务；压缩快照 |
| 模型适配 | `internal/agent/runtime/` | Fantasy 模型/工具配置、共享 HTTP Transport、响应头/流空闲超时、重试分类 |
| 内置工具 | `internal/agent/tools/` | `read/bash/edit/write` 默认项目工具；schema 校验、os.Root、原子文件写入、进程组取消 |
| MCP | `internal/service/system/mcp.go` → `internal/agent/mcp/manager.go` | 配置持久化和连接发布分离；动态启停、工具命名、调用超时 |
| 项目浏览 | `internal/service/project/` | 数据库项目目录、文件树/内容、文件引用搜索、Git 状态和 diff |
| 系统配置 | `internal/service/system/`、`internal/secret/` | 模型/提供商、TLS、凭据、使用量统计 |
| 前端 | `App.tsx` → `ChatProvider` → `useChat` → `api.ts` / `sse.ts` → `reducer.ts` → `AssistantThread` | 会话对象独立、切换页面不断流、应用卸载取消请求、历史分页 |
| 存储 | `internal/db/`、`internal/ent/schema/` | Ent 生成代码；全局 client；SQLCipher 单连接；不直接修改生成文件 |

一轮聊天的顺序是：解析模型 → 会话身份与并发限制 → 从数据库读取历史及项目归属 → 准备工具/指令/引用文件 → 新会话立即入库并启动独立标题任务 → 写入 `running` 轮次 → 启动 recorder → Fantasy 流和工具回调 → 停止 recorder → 完成事务或保存失败终态 → 事务成功后才发送 `done`。

前端每个 session 持有 stream/request/completion/title 控制器。导航只切换选中对象；这正是后台会话继续生成的基础，不能通过“切换时统一 abort”简化。

### 1.3 已有合理设计，应保留

- `activeConversations` 使用互斥保护 map，取消原因使用 atomic；全局聊天/标题槽位有上限。同一会话的完成写入还有数据库版本条件。
- SSE/WS 发送支持取消，WS 应用数据帧由单写 goroutine 串行发送；内置 bash 已配置进程组终止和 `WaitDelay`。
- `turnTrace` 使用锁；回调释放锁后再发送事件，未发现该路径的明确锁序环。`snapshot` 中共享的 ToolOutput 指针目前按创建后只读使用，不能仅因“浅拷贝”判定 data race。
- `secret.currentAEAD` 取出的 key slice 在发布后没有原地修改；`Init` 替换引用，未发现这里的 Go 内存数据竞争。真正问题是轮换跨数据库和内存状态不原子，见 O01。
- 文件读写使用 `os.Root`，写操作拒绝符号链接路径，临时文件同步后 rename；工具输入有 JSON Schema 本地校验。没有证据支持“所有文件工具均可直接通过 `../` 逃逸”。
- 提供商请求复用 Transport，已经有响应头超时和流式空闲看门狗；不建议再为每轮新建 Transport。
- Markdown 没有启用原始 HTML；模型图片需用户点击后加载；Mermaid 校验使用 strict；高亮有长度限制与延迟；图表/观察器大多有 cleanup。不能笼统认定整个前端存在订阅泄漏或 XSS。
- 历史分页、折叠工具详情按需请求、系统页面与大型图形库懒加载已经存在，不重复提出这些改造。

## 2. 优先级总览

风险描述影响，优先级描述实施顺序。P0：数据/凭据完整性应优先修复；P1：可靠性、安全边界与资源问题；P2：有测量门槛的性能或维护性改进。所有工作量为熟悉项目工程师的粗估，包含相关测试，不是工期承诺。

| 编号 | 问题 | 证据性质 | 风险 / 优先级 | 粗估 |
| --- | --- | --- | --- | --- |
| O01 | TLS 轮换与凭据重加密不原子 | 静态故障路径 | 高 / P0 | 3–5 人日 |
| O02 | recorder 在事务提交前推进已刷版本，快照与统计不一致 | 静态故障路径 | 高 / P0 | 1–2 人日 |
| O03 | Git 查看仍执行 textconv，子进程取消不足 | 隔离实验 + 源码 | 高 / P1 | 2–3 人日 |
| O04 | 延迟历史确认覆盖新轮次或当前分页 | 静态逻辑竞态 | 高 / P1 | 1–2 人日 |
| O05 | 停止操作可能 abort 后续轮次 | 静态逻辑竞态 | 高 / P1 | 1–2 人日 |
| O06 | 关停未统一覆盖 WS、新任务准入与落库预算 | 静态生命周期缺口 | 高 / P1 | 2–4 人日 |
| O07 | 项目文件预览可阻塞于 FIFO 打开 | 静态故障路径 | 中 / P1 | 0.5–1 人日 |
| O08 | bash 完整输出无磁盘配额，跨日期读取不一致 | 静态资源/兼容缺口 | 中高 / P1 | 2–3 人日 |
| O09 | 主机工具/MCP 权限依赖全局信任，无任务级强制边界 | 有条件安全风险 | 高 / P1 | 3–6 人日，分阶段 |
| O10 | 凭据加密失败降级、明文响应缓存策略不完整 | 静态安全缺口 | 高 / P1 | 1–3 人日 |
| O11 | MCP 本地参数契约校验弱于内置工具 | 静态校验缺口 | 中 / P1 | 1–2 人日 |
| O12 | MCP 配置全局锁覆盖网络连接和关闭 | 静态阻塞路径 | 中 / P2 | 1–2 人日 |
| O13 | 每个 token 复制/重渲染整条前端消息链 | 源码支持，收益待测 | 中 / P2 | 2–3 人日 |
| O14 | 前端 session 常驻增长，后台 token 也触发全局渲染 | 静态保留路径 | 中 / P2 | 1–2 人日 |
| O15 | 会话列表刷新并发重取所有已加载页 | 静态请求放大 | 中 / P2 | 1–2 人日 |
| O16 | 小范围 read 仍整文件读取，文本与上下文重复复制 | 源码支持，收益待测 | 中 / P2 | 2–3 人日 |
| O17 | SQLCipher 单连接上的统计和启动任务扩大阻塞 | 静态查询路径 | 中 / P2 | 2–3 人日 |
| O18 | 头像资源约 4.07 MB，远大于显示尺寸 | 构建实测 | 中 / P2 | 0.5–1 人日 |
| O19 | 网络类型断言缺少运行时验证 | 源码确认；当前编译器已默认 strict | 中 / P2 | 1–2 人日 |
| O20 | 日志出口不完全统一，错误内容与监控缺口 | 静态确认 | 中 / P1–P2 | 1–2 人日 |
| O21 | 项目说明与当前运行/构建行为不一致 | 源码与文档对照 | 中 / P2 | 0.5–1 人日 |

## 3. 数据一致性与生命周期

### O01：将 TLS 轮换和凭据更新变成一个可回滚操作

**位置与影响：** `internal/service/system/tls.go:112–200` 的 `TLSUpdate` 先读取/解密全部凭据，单独提交新证书，再 `secret.Init` 切换全局密钥，最后逐行更新凭据。中途请求取消、数据库写失败或进程退出会留下新证书和旧密文；重启后仍无法解密。并发 `ProviderCreate/ProviderUpdate` 或第二次 TLS 轮换还会遗漏新凭据或覆盖较新的 API Key。`tls_secret_test.go` 当前验证成功轮换及外部密钥，不覆盖中途失败。这里是业务原子性问题，`-race` 不会报告。

**推荐方案：** 增加可独立构造的不可变 cipher 对象；在内存中准备旧/新 cipher，但不要提前发布新全局状态。在一个数据库事务内读取需要迁移的凭据、保存新证书和全部新密文，成功提交后再切换活动 cipher。所有涉及凭据读取、加密写入、轮换的 service 操作使用同一协调边界：普通操作共享访问，轮换独占访问，覆盖“读数据库 → 解密/写数据库”整个短操作，不能只保护单次 AES 调用。先确定一致锁序，禁止持数据库事务等待反向锁。聊天只在获取配置和明文凭据时进入该边界，不把模型网络调用放进锁内。

**涉及文件：** `tls.go`、`provider.go`、`internal/secret/secret.go`、聊天/标题获取凭据的调用处及对应测试。无需更换 API，也不要求先引入多版本密钥存储。

**验证：** 单元测试新旧 cipher 独立；集成测试在第 N 条写入和 commit 注入失败，确认旧证书/全部旧密文仍可用；并发覆盖 create/update/reveal/chat 与轮换、两个轮换竞争；模拟事务提交后进程重启。回归外部密钥模式、不填 API Key 保留原值、掩码和既有错误码。

**兼容风险：** 现有 `enc:v1:` 必须继续可读；轮换成功后服务端 TLS 证书仍按原有“重启生效”行为，不混入热更新功能。对已经损坏的混合密文不能承诺自动恢复，应检测并要求恢复备份或重新录入。

### O02：修复增量记录器的提交水位与快照一致性

**位置与影响：** `internal/service/agent/chat_recorder.go:90–156` 在循环内更新 `r.flushedRev`，之后才 `tx.Commit`。后续 block 写入/commit 失败会回滚数据库，却不回滚内存水位。下一次 flush 可跳过从未成功保存的块，或对不存在的块执行 update；失败/中断轮次可能永久缺块。正常完成路径整体重写 blocks 可能掩盖缺陷。

另外，`snapshot()` 和 `stats()` 是 `chat_trace.go:70–84` 的两次独立加锁：两者之间若新增 delta，recorder 会把未包含在快照中的 revision 标成已刷。如果随后模型长时间静默，`shouldFlush` 认为没有变化，这部分内容不能按预期及时入库。

**推荐方案：** 快照一次返回 blocks、每块 revision、总 revision/toolRev/bytes；同一临界区内捕获。事务执行阶段把待推进水位放局部变量，只在 commit 成功后更新 recorder 的全部状态。检查已存在块 update 的影响行数；异常时明确失败，不把零行更新当成功。

**涉及文件：** `chat_recorder.go`、`chat_trace.go`、`chat_running_turn_test.go`、`chat_trace_test.go`，可新增聚焦 recorder 的测试文件。

**验证：** 使用 Ent hook/driver 故障注入，分别使第二块写入、commit 失败，再重试；验证块完整、无重复 sequence、水位不超前。用 barrier 确定性地在旧快照/统计窗口插入 delta，验证修复后没有漏刷。回归取消、断网、正常完成、重启 reconcile，运行 race。

**兼容风险：** 保留 schema、事件顺序和 1 秒检查/2 秒空闲/16 KiB 阈值；只修复确认保存的定义。不要把持锁范围扩大到模型回调或网络发送。

### O06：统一关停顺序、任务准入和最终落库预算

**位置与影响：** `internal/cmd/run.go:207–221` 先关闭 MCP，再取消标题并等待 HTTP；`internal/service/agent/concurrency.go:78–104` 没有 stopping 准入状态，`Shutdown` 只取消标题。`ChatWS` 的 context 来自请求，HTTP `Shutdown/Close` 不负责关闭已 hijack 的 WebSocket，现有 WS 可继续发起轮次；`WaitActive` 归零与后续 `Add` 之间也缺少统一生命周期协调。

recorder 正在执行的后台 flush 最长 15 秒，失败后的最终 flush/mark 各有独立超时，而 HTTP 和工作等待各只有 5 秒。进程可在工作仍落库时返回并关闭数据库。此为可推导的超时预算不匹配，未运行真实生产 SIGTERM 实验。[Go HTTP 生命周期文档](https://pkg.go.dev/net/http#Server.Shutdown)明确要求应用自行处理 WebSocket 关停。

**推荐方案：** 启动处持有 service 生命周期；先原子设置 stopping 并停止接纳新任务，再通知所有 SSE/WS 和后台任务取消，再等待 recorder/终态保存，最后关闭 MCP 和数据库。可用 HTTP `BaseContext` 传播应用取消，但仍要主动关闭/通知 WS 以打断读循环；保留连接注册表或现有层可实现的最小协调对象。WaitGroup 的注册发生在任务启动前且受准入门保护。最终落库使用独立但统一总预算，避免每个阶段重新领取完整超时。先测量再设定总预算，超时明确记录尚未结束的任务数量。

**验证：** 新增 `internal/cmd` 生命周期集成测试：SSE、空闲 WS、活动 WS、标题、MCP 调用和受阻数据库同时存在；shutdown 后不接受新轮次、连接关闭、工具子进程退出、终态尽可能落库后才关闭 client。覆盖多次 shutdown、监听失败、超时降级和 race。

**兼容风险：** 导航不影响服务生命周期；用户停止仍为 `canceled`，服务关停为 `interrupted`；独立最终落库 context 不能直接继承已取消的请求。

## 4. Agent 安全与资源边界

### O03：Git “只读”接口禁止外部执行，并控制进程树

**位置与影响：** `internal/service/project/git.go:48–69` 只用 `exec.CommandContext`，没有进程组终止或 `WaitDelay`。`gitDiffOutput:323–341` 设置了 `--no-ext-diff`，却没有 `--no-textconv`。配置了 diff textconv 的项目，在用户只查看 diff 时会执行转换程序；`.gitattributes` 可选择已有 driver。外部程序继承服务环境并可能持有 stdout/stderr，父 git 被取消后 `Wait` 仍可能受子进程管道拖延。`git status` 还应明确禁用配置的 fsmonitor hook。

**实验证据：** 在自动清理的独立临时 Git 仓库中配置无害 marker textconv，使用当前 diff 参数后 marker 被创建；追加 `--no-textconv` 后不创建。未访问真实用户仓库凭据，未修改本项目 Git 配置。两类参数的差异也见 [Git diff 官方说明](https://git-scm.com/docs/git-diff)。

**推荐方案：** 查看类 Git 调用统一禁用 external diff/textconv/fsmonitor；明确环境处理，过滤会改写仓库定位和执行行为的 `GIT_*` 环境变量，而不是继承未知的 `GIT_DIR/GIT_WORK_TREE`。采用受控进程组和有界管道等待；复用现有进程控制语义，只有确实被 tools/MCP/project 共用的部分才提取到小型内部包。单文件路径使用 literal pathspec，避免 `:(top)`、通配符等被解释成查询范围；词法 `cleanRelative` 和 `--` 不等于 Git pathspec 字面量化。

**验证：** 扩充 `git_test.go`：textconv/fsmonitor marker 不执行；恶意环境不改写目标仓库；含 `*`、`[`、`:` 和空格的实际文件名只匹配自身；项目位于仓库子目录；取消杀死同组子进程并按时返回；保留空仓库、重命名、二进制、未跟踪文件和截断行为。

**兼容风险：** 自定义 textconv 展示会变为原始文本/二进制提示，属于落实“只读查看”边界的必要收紧，须记录。不能声称这让整个宿主 Agent 成为沙箱。

### O07：文件预览避免在类型检查前阻塞

**位置与影响：** `internal/service/project/tree.go:145` 用 `root.Open(rel)`，之后才 `Stat` 判断普通文件。项目内 FIFO 无 writer 时，打开本身就可阻塞，context 取消无法直接中断。对比 `internal/agent/tools/process_unix.go:14` 已使用 `O_NONBLOCK` 打开再检查类型。

**推荐方案：** 项目预览采用同样的 root-relative 非阻塞打开、打开后 fstat、拒绝非普通文件的顺序；只做 lstat 预检查不足以抵御检查后的替换。保留 `os.Root` 边界和 512 KiB 限制。

**验证：** `project/git_test.go` 或新增 content 测试覆盖 FIFO、目录、设备型路径（适用平台）、符号链接逃逸、普通 UTF-8 文件、取消；API 集成测试在严格测试超时内返回错误。不能把阻塞 goroutine 包一层 select 当成修复。

**兼容风险：** 普通文件响应不变，非普通文件快速报错；网络文件系统自身的内核 I/O 挂起仍不保证被 context 中断。

### O08：为工具完整输出增加实际磁盘边界

**位置与影响：** `internal/agent/tools/bash.go:43–95` 只限制内存 tail；超出 50 KiB/2000 行后把后续所有字节写到 `/tmp/kaguya/<date>/<conversation>/`。`Set.Close` 只关闭 root，不删除文件，源码无输出总额/保留期管理。默认超时不能替代容量限制，且系统命令超时可达一天。

`tools.go:69` 每轮按当天生成目录，`read.go:74–91` 仅接受当前 Set 的日期目录：隔日续聊不能通过 `read` 读取前一天已返回的输出路径。超过 32 MiB 的完整日志又会被 `readRootBytes` 拒绝，即使请求小 offset/limit。

**推荐方案：** 明确单命令、单会话和总目录配额；达到阈值后终止命令并返回已保存 tail、截断原因及可用路径，不静默丢弃。输出目录由会话身份关联，读取可允许同会话的旧日期文件，但禁止跨会话读取。清理只处理本应用确认拥有的普通文件，不跟随链接，不删除活动输出；保留期到期给可解释错误。与 O16 一起实现大日志有界分段读取。

**涉及文件与验证：** `bash.go`、`tools.go`、`read.go` 及工具测试；用低配额替身测试大输出/磁盘满/部分写失败、并发命令、跨日同会话、跨会话拒绝、清理竞争。用隔离临时目录跑集成测试，不填满真实磁盘。

**兼容风险：** 保留 tail 格式、退出状态和旧路径解释；配额作为明确配置/运行约束渐进启用，不能因“优化”悄悄删除仍需续读的文件。

### O09：区分全信任宿主执行与可强制的任务权限

**证据：** `chat_prompt.go:43–54` 对项目注册全部 coding tools，并对所有对话追加 `Default.Tools()`；`tools/bash.go:151–153` 执行模型提供的 `bash -c` 且继承 `os.Environ()`；MCP stdio 也继承服务环境（`manager.go:65–75`）。`instructions.go` 自动把项目根 AGENTS 文本加入 system prompt。没有项目级工具白名单、一次任务的只读权限或执行授权记录。

**影响与界定：** 当前 README 明示这是无沙箱的单用户服务，因此 `bash -c` 本身是设计能力，不能把它误报为普通参数拼接注入。但恶意仓库指令、文件内容、MCP 描述/结果可以诱导模型读取项目外文件、服务密钥或向网络发送内容；文件工具的 os.Root 无法约束 bash/MCP。普通聊天“没有主机工具”也不能推出没有 MCP 副作用。

**推荐方案：** 保留既有 trusted-host 行为作为明确兼容模式；新增可选择的项目/任务工具集合，把“只读请求”落实为不注册 bash/edit/write 及未获授权 MCP 工具。MCP 的 `readOnlyHint` 等远端注解只能作为提示，不可作为授权依据；由本地配置建立允许范围。项目指令首次信任与快照来源可见，保持指令无法提升本地权限。敏感操作需要授权时，先做工具调用预览并在执行层校验授权，不能仅追加一句 system prompt。严格限制文件/网络能力需要单独执行身份或隔离运行器，列为后续可选能力，不在小修中假装实现完整 Bash 沙箱。

服务默认 loopback，`pkg/request_security.go` 提供浏览器来源隔离，**没有认证**。如果监听非 loopback，必须依赖已有受控反向代理，或作为独立增强引入最小访问凭据；Host/Origin 不能阻止能直连服务的非浏览器客户端。保留本地默认体验，不在本计划直接启用多用户登录。参考 [MCP 工具安全约定](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)。

**验证：** `project_tools_test.go`、`instructions_test.go`、`mcp_test.go` 增加能力矩阵；用假工具记录是否被执行，恶意工具结果不能提升能力；测试续聊请求不能改项目、同项目只读模式不能写、显式 trusted 模式保持现有工具。集成测试来源检查、远程入口认证配置和 API Key 查看权限。

**兼容风险：** 默认工具集合和已启用 MCP 不能无公告更改；先增加可选限制和界面可见性，再决定新项目默认策略。继承环境是当前工具链可用性的基础，最小环境模式需显式提供 PATH/工具所需变量，不能盲目清空。

### O10：明确加密失败策略和敏感响应缓存策略

**位置与影响：** `internal/cmd/run.go:148–156` 在 InitSecret 失败后继续运行；`secret.Init:59` 先清空 key，解析失败时保持无 key，`Encrypt:139–140` 会返回明文。显式外部密钥配置错误也会落入降级。注意 InitSecret 的失败也可能来自迁移阶段，此时 cipher 已启用，启动日志“一律已禁用加密”并不准确。

`internal/api/v1/system/provider.go:64–82` 的明文 API Key GET 没有 `Cache-Control: no-store`，而 MCP 详情已经设置。未发现已发生缓存泄露，但应消除秘密响应被缓存的条件。MCP Env/Headers 直接保存 JSON，Ent `Sensitive()` 不是字段加密；当前 SQLCipher 保护数据库页，不能将其误报成当前数据库文件明文。

**推荐方案：** 显式密钥无效时拒绝进入凭据写入状态；区分 cipher 初始化错误与历史加密迁移失败。若继续提供只读诊断页面，写凭据和模型调用应返回固定状态，不默默新增明文。对需要兼容的旧明文路径保留解密读取和显式迁移，不自动清空记录。明文 Key 及含认证信息的所有配置响应统一 no-store，前端 reveal 关闭时释放明文引用；不进入 localStorage 或通用错误日志。

**验证：** `secret_test.go`、`provider_secret_test.go` 增加无效显式 key、迁移失败、已有 cipher 不误清空测试；API 测试检查成功/错误响应缓存头。回归证书派生模式、外部 key、TLS 轮换、空 key 保持原值和 reveal。

**兼容风险：** 失败时写入限制是安全收紧，应通过清晰状态及部署说明迁移，不能让历史安装在无解释情况下变成不可用。字段级 MCP 加密不作为当前默认必做项；只有明确要求抵御解密导出泄露时再复用 O01 的 cipher 和迁移机制。

### O11：MCP 工具调用使用完整的本地输入验证

**位置与影响：** `internal/agent/mcp/manager.go:248–285` 将 schema 转为 Fantasy 的 properties/required；`Run:308–316` 仅验证输入是 JSON 对象就转发。根级 `additionalProperties/minProperties/maxProperties` 等约束不会成为本地检查，required 和字段类型也依赖模型或远端。对比 `internal/agent/tools/schema.go:82` 已有本地 validator。

**推荐方案：** 连接准备时保留并编译已接受的完整 schema；实际调用前本地验证，错误返回为工具错误且不触达远端。模型可见 schema 的转换与执行校验分开；不支持的 schema 仍明确拒绝。每轮适配器的 Info 返回独立可变树，避免 provider 对 nested schema 的规范化污染后续使用。不要仅为消灭 Go `map[string]any` 而重建 JSON 类型系统。

**验证：** `manager_test.go` 覆盖缺必填、错误类型、未知字段、数量约束、合法输入、远端调用次数为零；验证调用方修改 Info 不影响后续 Info。回归命名空间、三类 transport、停用/超时/取消。

**兼容风险：** 只强制工具已声明的约束，不自行猜测参数；旧服务声明与实际实现不一致时给明确兼容错误。结果 64 KiB 截断发生在完整反序列化和 marshal 之后，并非接收内存限额；后续接收层限额须按 transport 的单条消息实现，不能截断整个长连接流，收益和阈值经大结果测试确定。

## 5. 前端状态与性能

### O04：延迟历史确认必须绑定 session 的操作版本

**位置与影响：** `web/src/features/chat/useChat.ts:52–66` 延迟 3 秒后无 signal 调用 `fetchTurnPage`，只在请求前检查 mounted，返回后直接整体覆盖 `session.turns/page/totalPages`。等待期间用户可能切页、重新加载、发送下一轮或删除 session。旧确认可覆盖新 streaming turn，后续 frame 又以 `slice(0,-1)` 合并，进一步错乱当前页内容。卸载后已经发出的确认请求也不会被 cleanup abort。

**推荐方案：** Session 增加独立 reconciliation 控制器/定时器和单调操作版本。发送、分页、重载、forget、卸载使旧确认失效；返回时检查 controller、session 是否仍注册、版本及是否有新 stream。优先只按 turn_index 合并被确认轮次终态；确需替换整页时检查页身份一致。

**验证：** `hooks.test.tsx` 用可控计时器和 deferred Promise，覆盖确认响应晚于新轮次、切页、删除、卸载；只更新目标 turn，草稿/选择/新消息不丢失。集成模拟断网恢复与后台会话继续流式生成。

**兼容风险：** 保留一次延迟确认的稀疏请求策略，不变成无限轮询；不能为了防竞态取消其他 session 的 stream。

### O05：停止操作捕获原始控制器和轮次身份

**位置与影响：** `useChat.ts:258–270` 对停止请求和 800 ms 定时器做 Promise.race，finally 才读取 `item.stream?.abort()`。原轮次在等待中自然完成且用户发起下一轮时，finally 读取的是新 controller，可能取消新轮次。服务端 stop API 按会话标记当前 lease，延迟到达的旧 stop 请求也有同类身份问题。

**推荐方案：** 点击时立即捕获 controller 和本轮 generation；finally 只 abort 捕获对象，不读取可变 session.stream。清理停止计时器并约束未结束的停止通知。服务端增强可选的目标 turn/lease 标识，只有匹配时才标记 userStop；旧客户端省略标识维持原语义。若先做纯前端修复，须明确后端身份风险尚未完全消除。

**涉及文件：** `useChat.ts`、聊天 `api.ts`、`internal/api/v1/chat/conversation.go`、`concurrency.go`，若扩展请求则同步 DTO/Swagger。

**验证：** 用 A 完成→B 开始→A stop Promise 结束的确定性顺序测试 B 未被 abort；测试慢 stop API、重复停止、网络失败、无 ID 首轮、后端旧 stop 到达新 lease 的情况。回归 canceled/interrupted 分类和 800 ms UI 等待上限。

**兼容风险：** 不自动重试聊天 POST；不把终态轮次改为 canceled；新增字段可选，已有协议继续可用。

### O13：降低流式渲染中的全量复制

**位置与影响：** `reducer.ts:14–39` 每帧克隆所有 blocks，查找工具并重新统计数量；`useChat.ts:209–212` 每帧创建 turns 数组并 notify；`AssistantThread.tsx:11` 因 turns 引用变化重新映射全部 messages，Markdown 重解析随之发生。多工具长回答和多个后台流会放大主线程工作。尚未测得生产帧耗时，不给出虚构的百分比收益。

**推荐方案：** reducer 保持纯函数，只复制数组和实际变更的 block；未变 turns/assistant messages 保持引用。按显示帧合并 notify（例如 rAF），事件仍按序立即进入 session；done/error/取消前强制发布，后台标签页定时器节流时也不能丢终态。确认 profiler 热点后再 memo 消息组件，不先引入新的状态库或 Worker。

**验证：** reducer 原有帧测试保持；补旧对象未修改、非目标 block 引用稳定、同帧多个 tool/result 顺序测试。用 1/4/16 个假流、每流 50/200 delta/s、100/1000 blocks、100 KiB/1 MiB 正文测 React commits、长任务、输入延迟和最终文本；真浏览器核对滚动和后台标签恢复。

**兼容风险：** 只批量呈现，不批量改变持久化语义或事件顺序；不得重复消费兼容 content 与 block.text；保持复制原文、推理折叠和工具状态。

### O14：区分常驻会话元数据与可回收历史

**位置与影响：** `useChat.ts:35–36,83–97,252` 的 Map 长期保留所有访问过且有内容的 session，只有显式 forget 或特定空草稿会删除；没有容量或字节预算。`ChatProvider` 位于导航之上，长时间使用积累历史文本；后台任意 token 也调用同一个 render，构造全部 localSessions。

**推荐方案：** 先记录保留 session 数和估算文本字节，分离活动流/草稿/选择等小状态与持久化历史缓存。只对完成、非选中、无进行中请求且内容可从 DB 恢复的历史实施 LRU；保留草稿、引用、模型选择和最近会话入口。后台文本更新无需强制重渲染当前会话正文，列表仅在状态/标题变化时更新。

**验证：** hooks 测试访问超过预算的 session 后历史可重载；活动流和草稿不可回收；同一 ID 不创建重复 session。浏览器连续访问 100/1000 个会话后 heap 应在缓存预算附近稳定；不是要求 GC 立即归零。

**兼容风险：** 保留“切换页面不断流”及未保存失败会话可再次进入的行为，不直接对 Map 做简单长度截断。

### O15：减少列表刷新请求放大

**位置与影响：** `useConversations.ts:31–33` 非 append 刷新通过 Promise.all 重新请求 1..当前页的每一页；聊天 start/done 会触发刷新。加载 30 页后一次刷新就是 30 个并发请求，多个聊天完成时会反复 abort/restart；取消浏览器请求也不保证已执行的数据库工作被撤销。

**推荐方案：** 合并同一筛选条件的刷新请求；首次刷新重点更新第一页和受影响会话，再按可见范围更新。必须完整重取已加载区间时限制请求并发，保留去重、排序和标题 patch。先在现有 hook 内处理，不引入分布式缓存或新分页 API。

**验证：** 在 `hooks.test.tsx` 构造多页和多个同时完成事件，断言峰值并发和总请求数有界；保持收藏/关键词/项目切换、loadMore、重复 ID 去重、最新标题不倒退。API 集成对新增/删除引起的 offset 位移做回归。

**兼容风险：** 不能仅刷新第一页后把陈旧的后续页伪装成完整最新列表；保留用户位置，明确后台刷新状态。

### O18：缩小真实使用的头像衍生资源

**证据与影响：** 本次 `bun run build` 输出 `lyonmu` PNG 1,768.45 kB、`kaguya` PNG 2,301.21 kB，总计约 4.07 MB；`MessageList.tsx` 在头像和欢迎区域直接引用，CSS 显示尺寸远小于原图。已有 JS 拆包预算无法覆盖这笔资源成本。

**推荐方案：** 保留原始图片和角色形象，离线生成适配头像/欢迎区域的多倍尺寸压缩衍生图，使用现代格式并保留必要 fallback；明确 width/height 防布局偏移。无需图像生成模型重绘品牌。

**验证：** 更新资源预算测试，明确两张衍生图合计目标（建议先以 ≤200 KiB 为试验目标，视觉质量优先）；浏览器在 1x/2x、明暗主题和移动端核对清晰度、透明边缘；测冷缓存首屏网络字节。保留 `ChatPage.test.tsx` 的头像语义断言。

**兼容风险：** 原图仍保留，不替换为未经认可的新设计。产物是运行时静态资源，未来实施时与本分析报告、截图区分。

### O19：补齐网络边界的运行时类型验证

**位置与影响：** `api/http.ts:74,107` 直接将 JSON/data 强转为泛型，只检查 envelope 的部分字段；`sse.ts:19–32` 只粗检 flag，不检查 blocks/usage。坏响应可能进入 reducer。两套 tsconfig 虽未写 strict，但本地实际 TypeScript 6.0.3 已默认开启严格检查：通过编译器 API 实测，空配置的 strictNullChecks/noImplicitAny 都为 true。因此**不将未显式配置 strict 列为缺陷**；本次两套追加 `--strict` 也均通过，但静态类型检查不能验证网络 JSON。

**类型检查结论：** `web/src` 未发现显式 `any` 滥用。`FileTree.tsx:106,116` 的 `as unknown as TreeNode` 和 `MessageList.tsx:113` 的 metadata→Turn 是有实际边界含义的断言，应集中在适配器并校验必需字段；`main.tsx` root、Map.has 后 Map.get、明确初始化的 children 等非空断言不应机械删除。`useChat` 的 `turn!` 可随 O04/O05 改为更明确的本轮闭包变量。

**推荐方案：** 网络数据先接收为 unknown，增加小型类型 guard：ApiResponse 的 code/message、需要 data 的接口、SSE frame/block/usage 的关键字段；允许未知扩展字段，void 响应允许无 data。复用现有类型，不新增庞大 schema 依赖。继续保持当前 strict 行为，不同时大规模开启所有更激进 flags，也不为“开启 strict”制造无实际收益的配置提交。

**验证：** HTTP/SSE 单测覆盖 null、数组 data、错误枚举、缺少 usage、非字符串 delta、合法额外字段；断言错误可展示且不破坏当前 session。运行 test/lint/build、两套 strict 检查。更新测试中的异步 act 包装，避免靠忽略 console.error 隐藏竞态。

**兼容风险：** 保留旧 content 兼容帧、可选字段和取消异常；不要把所有第三方回调类型断言一律换成运行时报错。

## 6. 后端性能与并发细化

### O12：MCP 配置改用按服务隔离的可取消串行化

**位置与影响：** `internal/service/system/mcp.go:96–119,122–151,177–209` 以全局 `mcpMutation` 包住 Prepare 网络连接及 Replace/Close。单个故障服务最长连接准备约 15 秒，在此期间其他 MCP 的停用、删除、修改也等待；mutex 等待不响应请求 context。这不是已证实的死锁，而是跨服务队头阻塞。

**推荐方案：** 按 MCP ID 建立可取消的短生命周期串行门，同一服务仍按“准备 → 保存 → 发布”顺序；创建名称唯一性由数据库约束保障。锁引用计数回收，避免 lock map 永久增长。若后续将连接准备移到锁外，必须引入版本核对并关闭失效候选连接，不能先解锁就直接发布旧配置。启动恢复仍可顺序执行；不必先改为全量并发启动。

**验证：** A 的连接阻塞时 B 可及时停用；同 ID update/enable/delete 不倒置；等待者取消后不再修改 DB/发布连接；Prepare 成功但 DB 失败关闭候选连接。现有 MCP transport/race 测试全部回归。

**兼容风险：** 保持失败保留原配置、停用使旧 adapter 失效，不能用连接缓存延迟兑现用户停用。

### O16：优化有证据的内存复制，而非泛化“指针优化”

**位置与影响：** `read.go:33–67,118–142` 先完整读取最多 32 MiB，再转 string、Split/Join，即使只需少数行；多次 offset 读取重复扫描并分配。`chat_trace.go:119–169` 用字符串追加保存每个 delta；`compaction.go:29–45` 每一步序列化全部消息估算 token，随后有 provider usage 时又覆盖该估算。

**推荐方案：** 文本 read 流式扫描/计数，仅缓存所需窗口并保留最大行长保护；若要保留当前“总行数、整个文件 UTF-8 合法性”语义，仍可全文件扫描但不全量驻留，不能偷偷只检查前几行。图片路径保持现有大小/像素上限。trace 先用 alloc profile 确认追加成本，再考虑分块缓冲；snapshot 必须是稳定不可变数据，避免 strings.Builder 与数据库序列化共享可变底层内存。token 估算先判断是否有可用实际 usage，再计算 fallback；缓存仅针对不可变消息，发生压缩后正确失效。

**涉及文件：** `read.go`、`chat_trace.go`、`compaction.go` 及对应测试；与 O02、O08 有依赖。

**验证：** read 的空文件/末尾换行/CRLF/UTF-8 边界/长行/非法字节在远端行/图片/文件增长/取消；压缩保留系统指令、tool call/result 配对、原始历史和 usage。新增基准对 1/8/32 MiB 文件及 1k/10k/100k 小 delta 测 `B/op`、`allocs/op`、CPU；先比较基线后定目标。逃逸分析只用于定位基准热点，不以“所有对象不逃逸”为目标。

**兼容风险：** 不把文本块最终快照改为可变引用；不为性能丢失推理签名、ProviderOptions 或完整历史；不改变正常 read 的输出截断契约。

### O17：缩短单连接数据库的竞争路径

**位置与影响：** `internal/db/db.go` 的 SQLCipher 明确 `SetMaxOpenConns(1)`；`system/usage.go:60–115` 一个请求包含所选区间日聚合、distinct 会话数、固定一年活动和两次全历史构成聚合。修改时间范围时固定部分也重复计算。`kaguyachatturn.go` 当前有 conversation/index 唯一索引和 finished_at 索引，不直接假定还需要多个大索引。

启动的 `Schema.Create(context.Background())` 无期限；`conversation.go:158–169` reconcile 全量取 running IDs 再逐条调用独立 10 秒 context，调用者 30 秒初始化预算不能约束整个循环。这是启动可控性缺口，应与查询优化分别提交。

**推荐方案：** 对有代表性 SQLCipher 数据先测等待时间/查询计划；优先复用同请求中可共享的结果。固定活动和全历史统计如有缓存，必须有容量、短 TTL 或成功提交后的版本失效，响应定义不变。不直接扩大 SQLite 写连接数。索引只根据 `EXPLAIN QUERY PLAN` 和写入成本决定。初始化传入统一 context；reconcile 分批/集合更新，保持每行 duration 正确且中途取消可恢复。

**验证：** 使用实际 SQLCipher 临时库，1万/10万/100万轮次，混合 1/8/16 聊天、历史翻页和 usage 请求；记录 DB 等待、flush 延迟、P50/P95。回归 UTC 日界、固定一年、累计构成和仅 completed 计数；缓存不能纳入失败轮次。启动集成覆盖慢迁移、大批遗留 running、取消和重启幂等。保留遗留驱动测试，但不把 PostgreSQL 性能验证算作当前有效部署验证。

**兼容风险：** 数据统计口径、软删除行为及历史分页保持；任何 Ent 索引变化先改 schema 再生成，执行旧库迁移和回滚验证。不得用关闭同步/加密来换取 benchmark 分数。

## 7. 日志、抽象与文档

### O20：统一运行日志，记录结构化错误类别和缺失指标

**核查结果：** service/API 大多使用 `global.Logger`，内置工具注入其派生 zap Logger，符合约定。例外是 `pkg/gin.go:19–22` 的默认 `gin.Recovery()`/debug `gin.Logger()` 走 Gin 默认 writer，而不是全局日志；`http.Server` 未设置 ErrorLog。默认 panic 恢复/调试访问日志还可能带请求细节，需控制脱敏。

`chat.go:216` 将用户取消/断网统一 Error；`chat_execute.go` 的 OnRetry 和标题失败记录上游 err 文本。已核对 Fantasy v0.33.2 的 `ProviderError.Error()` 返回 Title/Message，不会自动输出整个 RequestBody，不能误报成完整请求体已泄露；但上游 message 可包含回显敏感数据。MCP SDK 日志被 io.Discard 避免泄露合理，但 MCP tool 成败缺少安全的本地摘要事件。`pkg/metrics.go` 只有 Go/process collectors，没有聊天槽位、数据库等待、flush、tool 结果等业务指标。

**推荐方案：** 在入口注入 zap 驱动的 recovery/access/http error 输出；HTTP 访问只记路由模板、状态、耗时、请求 ID，不记 query/body/认证头。错误在拥有上下文的边界记录一次；取消用 Debug/Info，短暂重试 Warn，终态失败 Error。工具与 MCP 记录工具本地 ID、call ID、耗时、结果类别，不记原始命令/参数/输出。加入低基数指标：活动/拒绝聊天、flush 成败与耗时、工具耗时/错误类、数据库等待、shutdown 未完成数；conversation_id/call_id 只放日志，不能作为 Prometheus label。

**临时输出清单：** `main.go:24` 是版本输出，`cmd/run.go:164` 是公开证书导出，logger 初始化前 stderr 是必要兜底；SSE 的 fmt.Fprintf 是协议编码、其他 builder 写入是字符串构造，均不应替换为日志。`web/src` 未发现 console.log。受版本控制的 `web/tmp-verify-heatmap.ts:13,21–22` 有 console.log，但属于离线验证脚本，不是产品代码；本次不删除/修改，未来处置需遵循生成资料授权约定。

**验证：** zap observer/缓冲 writer 测试各错误类别、日志级别与脱敏，注入带秘密 marker 的 provider 错误和 panic 请求；日志中不可出现 marker。测试 MCP 超时摘要及指标增减，断网/停止不产生大量 Error；不以关掉日志来通过测试。

**兼容风险：** 用户可读 API 错误码不变，CLI 版本/证书 stdout 格式不变；现有运维解析日志字段需要迁移说明。

### 架构与 interface：只在上述故障边界增加抽象

- 保留 API→service→runtime/tools 的现有分层、Fantasy 和 Ent，不新增通用 Repository 包装所有生成 CRUD。
- `AgentSvc` 为空结构体、service 依赖 `db.EntClient/global.Logger` 的主要代价是生命周期和测试替换边界。O01/O06 可引入最小 cipher/coordinator/生命周期依赖，其他模块按触及范围逐步注入；避免一次修改全部构造函数。
- 本地工具与 MCP 都实现 Fantasy AgentTool，复用共同协议即可；参数验证、进程控制等真正相同的机制再提取。MCP 连接管理与项目文件工作区不是同一种资源，不强行合成一个管理器。
- 对闭包、slice/map、接口返回值不做机械指针化。保留每轮 tool adapter/trace 的所有权；跨 goroutine 数据只读或按锁保护。新的 goroutine 必须说明启动者、取消者、等待者，不用无限 channel 缓冲掩盖慢消费者。
- 对临时/未完成的异步调用优先使用确定性 barrier 测试。不要仅依赖增加重复执行次数发现逻辑竞态。

### O21：同步真实运行契约，避免旧说明指导错误操作

**位置与影响：** 根 AGENTS 的数据库、安装位置和测试描述与实际代码存在差异；README.zh.md 的“遗留 Docker/PostgreSQL”章节虽然注明禁用，内部仍使用“当前对话存储在 PostgreSQL”等现在时。README 对 TLS 轮换的保证未说明非原子故障窗口。`useChat.ts:176` 注释声称失败/取消轮次不属于持久化历史，与当前实现冲突。

**推荐方案：** 在对应修复落地后同步 README.md/README.zh.md 的架构、安全边界、停止/断联、输出配额、凭据轮换和日志；旧部署段落统一标为历史事实，不能当现行安装步骤。AGENTS 的持久约定更新需单独确认新的运行方向，本方案不擅自选择数据库迁移。修正受影响源码注释，不顺带格式化无关模块。API 注解变化时按 `internal/cmd/run.go` 顶部命令同步 Swagger。

**验证：** 中英文功能/默认值/命令一致；检查链接、代码块与 `git diff --check`；在隔离环境按 README 的有效路径完成构建、CA 准备和启动冒烟。相关行为用前述单元/集成测试证明，纯文档无需新增镜像式测试。

**兼容风险：** 保留遗留资料和数据目录；不自动重新启用 PostgreSQL/Docker，不改变运行时默认值。

## 8. 参考设计及采用范围

| 参考 | 采用的具体做法 | 不照搬的内容 |
| --- | --- | --- |
| [Fantasy（Crush 使用的 Go Agent 框架）](https://github.com/charmbracelet/fantasy) | 延续模型/工具统一接口，在包装层加权限、验证、生命周期，不重写 provider 循环 | 不因框架示例简短就移除本项目持久化、取消、重试保护；运行语义以本地 v0.33.2 为准 |
| [Go net/http](https://pkg.go.dev/net/http#Server.Shutdown) | 显式管理升级连接的关闭与等待，应用统一持有 shutdown 协调 | 不把普通 HTTP shutdown 当作全部后台工作的回收器 |
| [React useEffect](https://react.dev/reference/react/useEffect) | 请求 cleanup 与忽略过期响应同时使用；由操作身份约束异步结果 | 不为了使用某种状态库而重写所有 hooks |
| [MCP tools 规范](https://modelcontextprotocol.io/specification/2025-06-18/server/tools) | 输入验证、调用可见性、本地信任决策；远端注解不等于权限 | 不立即实现完整 prompts/resources 或企业权限平台 |
| [Git diff](https://git-scm.com/docs/git-diff) | 只读查看同时约束外部 diff 与 textconv，按 Git 语义处理路径 | 不把 argv/`--` 误认为禁止所有外部行为 |

外部资料用于核对设计与库约定；本项目问题的位置和存在性仍由本地源码决定。未依赖无法获取的参考项目内容提出结论。

## 9. 本次验证结果与未验证项

| 实际执行 | 结果 | 说明 |
| --- | --- | --- |
| `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh test -race -count=1 ./...` | 通过 | 使用已有 SQLCipher 静态库，等价于 make test 的 Go 测试阶段；不重新构建 native 依赖 |
| `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh vet ./...` | 通过 | 无 vet 报告 |
| `cd web && bun run test` | 88 pass / 0 fail，24 文件 | 有 React act 警告及 happy-dom 不支持 Notification 的提示；不计作产品失败，也不隐藏 |
| `cd web && bun run lint` | 通过 | oxlint |
| `cd web && bun run build` | 通过 | TypeScript + Vite；观察到两张约 4.07 MB PNG |
| app/node tsconfig 追加 strict、noEmit、incremental false | 两者通过 | 未修改 tsconfig；另通过编译器 API 确认本地 TypeScript 6.0.3 已默认 strict |
| 独立临时 Git textconv 实验 | 确认执行；补 no-textconv 后不执行 | 无害 marker，临时仓库已清理 |

第一次直接调用 SQLCipher wrapper 因当前环境 `CGO_ENABLED` 非 1 被脚本拒绝；显式设为 1 后完成测试，不是业务测试失败。

本次没有修改测试或修复缺陷；没有运行真实模型/外部 MCP、生产数据库压力测试、真实服务 SIGTERM 测试、浏览器 heap/CPU 分析、完整 `make build/install` 或 Docker。未作依赖漏洞数据库审计，也不据版本号断言存在 CVE。测试和构建只产生工具缓存/构建目录，本次交付和版本控制变更仅为本文件。

## 10. 实施、验证与验收顺序

### 10.1 推荐提交序列

1. **阶段 A：完整性。** O01、O02 分开提交，每项先增加失败路径回归测试，再修复。验收：失败不损坏凭据、flush 重试不丢块；保持 SSE done 必须在 commit 后。
2. **阶段 B：已定位安全与状态缺口。** O03/O07、O04、O05、O06 分别实施；补确定性竞态和进程生命周期测试。O10 的 no-store 可独立作为小提交，加密失败策略与 O01 协同。
3. **阶段 C：资源和工具契约。** O08、O11、O12；O09 先写清兼容模式和权限矩阵，新增限制以可选择方式上线。涉及 API 新字段先后端兼容，再前端使用。
4. **阶段 D：有基线的性能改进。** 先 O20 的必要指标，再 O13–O18；每一项提交附基线/改后同条件测量。没有收益或影响正确性的优化不合入。
5. **阶段 E：固化工程约束。** 完成 O19 网络验证并维持当前 strict，O21 随行为同步，而非最后一次性重写文档。复核所有旧会话/配置、构建和升级路径。

### 10.2 每次实施的最低命令

在仓库根执行；前端命令按所在目录运行。不安装新的 Go/Bun 全局工具，不替换锁文件。

```sh
# 定向验证后，合入前完成现有全量门禁
make test
CGO_ENABLED=1 bash scripts/go-sqlcipher.sh vet ./...

cd web
bun install --frozen-lockfile
bun run test
bun run lint
bun run build
./node_modules/.bin/tsc -p tsconfig.app.json --noEmit --incremental false --strict
./node_modules/.bin/tsc -p tsconfig.node.json --noEmit --incremental false --strict
cd ..

make build
git diff --check
```

`make build` 会重新生成前端嵌入资源，只在实施阶段运行并检查产物；本次未运行。不得执行 `make install` 修改宿主安装来验证普通改动。

增加相应 benchmark 后，在同一机器、相同 CGO/加密设置下运行：

```sh
CGO_ENABLED=1 bash scripts/go-sqlcipher.sh test ./internal/agent/tools ./internal/service/agent -run '^$' -bench . -benchmem -count=5
# 对热点包单独采集，避免多个包共用 profile 输出路径
CGO_ENABLED=1 bash scripts/go-sqlcipher.sh test ./internal/service/agent -run '^$' -bench . -benchmem -cpuprofile /tmp/kaguya-audit-cpu.pprof -memprofile /tmp/kaguya-audit-mem.pprof
go tool pprof /tmp/kaguya-audit-cpu.pprof
```

benchmark 需先存在，不能把“无 benchmark 的空运行成功”计作性能验证。CPU/内存、mutex/block profile 只对确认热点采集；逃逸诊断可用定向 `-gcflags='-m=2'`，不作为零告警门禁。

### 10.3 跨模块回归矩阵

| 场景 | 必须保持的结果 |
| --- | --- |
| 首轮、续聊、无默认模型、模型删除 | 现有错误码/默认选择；不可用配置不执行工具 |
| 普通会话、项目会话、失败首轮后重试 | 项目归属来自 DB；现有 MCP 注入规则在兼容模式保持 |
| 多会话并发、同会话 SSE/WS 竞争 | 16 槽位快速拒绝；同会话互斥；无重复完成或工具执行 |
| 导航、断网、主动停止、shutdown | 导航不断流；不同取消原因正确；部分内容保留；无遗留子进程 |
| 工具失败、step_limit、压缩、重试 | 用户消息保留；半截助手/工具不进下轮上下文；不重放已有副作用 |
| 持久化中途失败、重启 | 无错误 done；可解释的终态/恢复；版本与块水位不超前 |
| TLS/Key/MCP 配置变更 | 旧密文可读、失败不混合状态、停用立即禁止旧适配器继续调用 |
| 文件浏览/编辑 | 路径边界、中文/空格/特殊字符、BOM/CRLF、权限、原子替换和截断保持 |
| 历史/统计/前端 | 分页和折叠详情正确；UTC 与 completed 统计口径不变；迟到响应不覆盖新状态 |

发布前备份数据库和独立密钥；涉及 schema 的变更在副本上验证迁移，并保留可回退版本。不得回滚到不能读取新密文/schema 的二进制；若只变更内部状态协调/响应头，应避免引入不必要迁移。每个实施 PR 使用中文 Conventional Commit，说明实际改变、验证结果和剩余风险；本方案和测试截图不默认暂存或提交。

## 11. 实施进度与剩余项

本节记录按上述顺序实施的结果；已完成项附对应提交方向，未完成项说明原因与剩余风险。实施只按方案范围修改，未引入数据库、鉴权或框架迁移。

### 11.1 已完成（含验证）

| 编号 | 状态 | 实施内容 | 验证 |
| --- | --- | --- | --- |
| O01 | 完成 | `secret` 增加不可变 `Cipher`，`TLSUpdate` 独占凭据锁并在单事务内保存证书与重写全部密文，提交后 `Publish`；无效显式密钥不再清空活动密钥 | `internal/secret`、`internal/service/system` 新增失败回滚、重启可读、密钥保留测试；全量 race 通过 |
| O02 | 完成 | `snapshot` 单临界区返回块与聚合统计；`flush` 用局部变量收集水位，commit 成功后才写回；更新路径校验影响行数 | `chat_recorder_test.go` 故障注入覆盖部分写失败、commit 失败、快照并发 delta、块丢失 |
| O03 | 完成 | 查看类 git 命令禁用 textconv/fsmonitor、过滤 `GIT_*` 环境变量、独立进程组 + `WaitDelay`、字面仓库相对路径 | `git_hardening_test.go` 覆盖 textconv/fsmonitor 不执行、环境变量不改写目标、特殊文件名、子目录项目 |
| O04 | 完成 | session 增加 `opVersion` 与 confirm 控制器，发送/切页/重载/删除/卸载使其失效；确认只按索引更新目标轮次 | `hooks.test.tsx` 新增延迟确认失效与停止竞态用例 |
| O05 | 完成 | `stop` 点击时捕获 controller 与身份，finally 不再读取可变的 `session.stream` | 同上；旧实现下用例失败 |
| O06 | 完成 | `workLifecycle` 统一准入/取消/等待；WS 连接注册表在关停时主动关闭；HTTP `BaseContext` 传播应用取消并注入 `ErrorLog`；关停顺序先取消再等待落库 | agent/api 生命周期测试；全量 race 通过 |
| O07 | 完成 | 预览改用非阻塞打开后 `fstat`，FIFO 等非普通文件快速报错 | `content_fifo_test.go`、`content_boundary_test.go` |
| O08 | 完成 | 单命令 64 MiB / 单会话 256 MiB 配额，达限终止命令并说明；同会话旧日期输出可读，跨会话仍拒绝 | `tools_test.go` 新增跨日期读取与配额终止用例 |
| O10 | 完成 | 提供商列表/详情/明文 Key 响应统一 `Cache-Control: no-store`；启动日志不再声称明文降级 | `provider_cache_test.go`；加密失败策略随 O01 收敛 |
| O11 | 完成 | MCP 连接时编译完整 schema，调用前本地校验；`Info` 返回独立 schema 副本 | `manager_test.go` 新增校验、schema 隔离、不支持 schema 拒绝用例 |
| O12 | 完成 | 按服务 ID 的引用计数串行门替代全局 mutex，等待可取消；新建不再持锁 | `mcp_test.go` 隔离、取消与锁回收用例 |
| O15 | 完成 | 刷新只重取首页与末尾页，分页请求固定并发；加载函数稳定引用 | `hooks.test.tsx` 30 页刷新请求数与并发上限用例（旧实现失败） |
| O16 | 完成（文本 read 部分） | 文本读取改为流式扫描，只缓存 offset/limit 窗口，图片先嗅探文件头；trace 与 compaction 未改 | `TestReadStreamingTextBoundaries`；benchmark 显示 8MiB 文件读取分配从约 8.5MiB 降至约 160KiB |
| O18 | 完成 | 144/288 WebP 衍生图 + `srcSet`，favicon 改 64px WebP，原图保留但不进构建 | 衍生图合计 48 KiB；`vite.config.test.ts` 增加预算与大图检查 |
| O19 | 完成 | HTTP envelope 与 payload、SSE 帧/块/usage 增加小型运行时 guard；`get/post/put` 接收可选校验器，坏响应不再强转 | `src/api/http.test.ts`、`sse.test.ts` 新增 null/数组/错误类型/缺失 usage/额外字段用例；两套 `tsc --strict` 通过 |
| O20 | 部分完成 | HTTP `ErrorLog` 接入 zap；`fmt.Print`/SSE 编码等必要输出保持；加密不可用日志修正 | `go vet`、全量测试 |
| O21 | 部分完成 | README 中英文同步 TLS 轮换原子性、输出配额与跨日期边界；源码注释同步 | `git diff --check`、双语对照 |

### 11.2 未实施项与原因

- **O09（任务级工具权限）**：需要项目级工具白名单、界面开关与权限矩阵，属于新功能而非缺陷修复，且会改变默认工具集合。当前保持既有 trusted-host 行为；方案中的“只读请求不注册 bash/edit/write”未实现，剩余风险与 1.2 节界定一致。实施前需确认新项目默认策略。
- **O13 / O14（前端渲染与会话缓存）**：方案要求“先有基线测量再定目标”。本环境未采集生产帧耗时与 heap profile，因此未做状态结构重写；现有实现与测试全部保持通过。实施前应先在真实浏览器与 profiler 下确认热点。O16 的 read 部分已完成，`chat_trace` 追加缓冲与 `compaction` token 估算仍未改动（需要 alloc profile 确认收益）。
- **O17（单连接数据库竞争与启动初始化）**：需要代表性 SQLCipher 数据集测等待与查询计划，未在本次环境构造。`ReconcileRunningTurns` 仍未接收统一启动 context 预算。
- **O20 未完成部分**：业务指标（聊天槽位、flush、工具耗时、数据库等待、shutdown 未完成数）未加入 `pkg/metrics.go`；访问日志仍由 Gin 默认 writer 输出。
- **O21 未完成部分**：`AGENTS.md` 的数据库、安装位置与测试描述仍与实际代码存在差异，按方案约定需要单独确认运行方向后再更新；Docker/PostgreSQL 遗留段落未改动。

### 11.3 实施期间的实测结果

- 每个阶段提交前执行 `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh test -race -count=1 ./...`，全部通过；`vet ./...` 无报告。
- 前端阶段执行 `bun run test`（最终 92 pass / 0 fail）、`bun run lint`、`bun run build` 与两套 `tsc --strict`。
- `make build` 已执行：前端构建、资源嵌入与 `target/kaguya` 后端二进制均成功；`make install` 未执行。未运行真实模型、外部 MCP、生产数据库压力或 SIGTERM 实验。
- `git diff --check` 无输出；提交按阶段拆分，均为中文 Conventional Commit。
- `web/src/features/chat/useConversations.ts` 存在 4 条 oxlint `exhaustive-deps` 警告：`fetchPage`/`loadPages` 未加入依赖数组（两者本身稳定），以及筛选状态被标为 load 的“多余依赖”（实际用于筛选变化时重建加载函数）。已尝试重构为 ref + 稳定回调但导致既有测试失败，故保留能通过全部行为测试的版本，未通过禁用规则掩盖。
