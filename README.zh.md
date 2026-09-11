<p align="center">
  <img src="images/kaguya.png" alt="Kaguya" width="120" />
</p>
<p align="center">带有 Web 控制台的 Go 原生 AI Agent</p>
<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh.md">简体中文</a>
</p>

# Kaguya

**Kaguya** 是一个使用 Go 构建的实验性 AI Agent 应用，将流式对话、模型配置、历史持久化和 Token 用量分析整合到一个 Web 控制台中。React 前端嵌入 Go 二进制，由同一个服务提供界面和 API。

项目面向学习、个人使用和 Agent Runtime 设计探索。当前控制台为中文界面；中英文 README 描述相同的功能，并使用同一组截图。

![对话工作区：历史列表、模型选择与 Markdown 回答](images/screenshots/chat.jpg)

## 功能概览

| 模块 | 可以做什么 |
| --- | --- |
| 流式对话 | 通过 SSE 接收回答，查看 Markdown 与提供商返回的思考内容，停止生成，为请求选择模型；同时提供 WebSocket API。 |
| 并行对话 | 生成时可新建或切换对话；各会话独立接收结果和停止生成，切到系统页面也继续执行。同一会话一次只执行一轮。 |
| 对话管理 | 继续已保存的对话，按标题前缀搜索，重命名和删除对话，按项目组织对话，分页浏览历史并查看轮次摘要。 |
| 提供商与模型 | 在界面中管理提供商及其模型，配置完整请求 URL、API Key、协议类型和模型元数据。 |
| MCP 管理 | 在 AI 配置页面管理 MCP 服务，支持 stdio、Streamable HTTP 和 SSE，动态启停并将工具接入聊天。 |
| 系统配置 | 分别选择默认对话模型和后台任务模型，追加自定义系统提示词，配置上游请求的 `User-Agent`；保存后新请求立即生效，无需重启。 |
| Token 用量分析 | 查看累计用量、日峰值、活跃会话、活动热力图，以及按模型或提供商划分的 Token 构成。 |
| 部署与开发 | 原生单二进制运行，使用 SQLCipher 加密 SQLite 并启用 WAL，无需 Docker 或数据库服务，提供 Swagger 与 Prometheus 端点。 |

## 界面与使用方式

截图采集于 **2026-09-09** 的运行实例。图中的模型名称、元数据、对话和用量均为该实例的示例数据，不是项目内置默认配置，也不代表上游模型的可用性承诺。模型截图裁取了管理抽屉区域，避开提供商凭据和端点详情。

### 1. 对话工作区

**项目管理：** 侧栏以文件夹标题分组展示多个项目，下方缩进显示各自对话并高亮当前对话；项目行菜单提供新建对话、编辑和删除。项目名称可重复，但未删除项目的规范化绝对路径必须唯一（符号链接别名也视为同一路径）；删除项目后可重新使用该目录。选择服务器已有文件夹时，从程序运行用户的 `~/` 开始（例如 `/root` 或 `/home/ubuntu`）；不能选择文件或越界目录，包括指向边界之外的符号链接。进入目录即选中当前目录，点击确认即可创建项目，无子目录时不显示空文件夹提示。一个项目可以包含多个对话，打开项目后新建对话，在首轮成功完成后建立关联。普通对话仅在 **对话** 列表中，项目对话仅在对应项目下展示，两类列表不混合。删除项目仍沿用解除归属并保留历史的行为，原项目对话转为普通对话，不删除主机文件。项目对话默认启用 `read`、`bash`、`edit`、`write` 四个编码工具，以项目目录作为工作目录；普通对话与标题生成不启用主机工具。Docker 环境中的路径属于容器运行用户的主目录，需要访问宿主机文件夹时应将其挂载到该目录下。

通过左侧列表新建或打开历史对话，按标题前缀搜索，或切换 **对话**（仅普通对话）和 **项目** 列表。对话顶部提供重命名和删除操作。输入框可选择提供商及模型，也可使用默认模型：**Enter** 发送，**Shift + Enter** 换行，生成中点击 **停止** 可取消回答。

用户消息气泡随文字长度收窄，长句和连续字符自动换行。Markdown 图片需点击后加载，避免模型生成的图片 URL 自动向外发送请求。回答支持 Markdown、代码块，以及提供商返回时可折叠查看的思考内容。轮次摘要显示 Token、耗时和工具调用次数。用量和模型窗口数据齐备时，输入框显示最近一次模型调用的上下文占用（输入含缓存，加输出），相对于系统配置的有效窗口（默认模型窗口的 90%）的占比，不再累计各轮费用。每次模型调用前检查系统配置的压缩阈值，自动总结较早内容并保留近期消息、工具调用配对和原始历史；压缩快照随成功轮次事务保存，续聊从快照恢复。生成遵守模型配置的输出上限和按压缩比例预留的剩余窗口。窗口未知时不自动压缩。超长历史按受限大小分段总结，再逐步合并摘要，避免总结请求本身超出模型窗口。摘要使用当前聊天模型且不带工具，摘要消耗计入本轮用量；失败或无法安全压缩时明确报错。

只有成功完成并保存的轮次会成为可继续使用的对话历史。失败或取消时的部分回答不会保存为完整轮次。配置后台任务模型后，界面会根据首轮成功的提问和回答请求生成简短中文标题，并保留手动设置的标题。

历史列表和聊天内容分别分页：侧栏虚拟列表每次读取 20 个对话摘要；聊天正文每页读取 5 轮，翻页替换当前页。前端使用 `compact=true`，完整返回用户消息和回答正文，思考与工具块仅返回状态等元信息，展开时才加载该块的完整内容；关闭未完成的加载会取消请求，失败可重试。展示查询不读取模型消息或压缩上下文。轮次接口默认 `limit=5`，保留最大 100 轮及未启用 `compact` 时返回完整内容的兼容行为。分页控制轮数而非字节数，单条超长正文或主动展开的大块内容仍可能较大。

### 2. 提供商与模型

打开 **系统管理 → AI 提供商** 新增提供商，再进入对应的 **模型管理** 抽屉添加模型。

![模型管理抽屉：模型标识、推理元数据与上下文窗口](images/screenshots/models.jpg)

提供商支持显式选择以下三种 API 协议：

| 协议 | 配置值 | 完整请求 URL 示例 |
| --- | --- | --- |
| OpenAI Chat Completions | `openai-chat` | `https://api.example.com/v1/chat/completions` |
| OpenAI Responses | `openai-response` | `https://api.example.com/v1/responses` |
| Anthropic Messages | `anthropic` | `https://api.example.com/v1/messages` |

请将示例域名替换为提供商的实际端点。**请求 URL 必须包含完整端点路径**：Kaguya 原样使用配置，不会自动追加 `/chat/completions`、`/responses` 或 `/messages`。提供商类型包含标准（`normal`）和 OpenCode Go（`opencode-go`），后者会附加 OpenCode 会话请求头。

每个模型包含显示名称、上游模型标识，以及推理等级、上下文窗口、最大输出 Token、Tool/Vision/JSON 能力等元数据。这些字段用于描述模型，本身不会启用附件、注册工具，也不保证每项参数都会传递给上游 API。聊天 API 选择模型时使用的是 **本地模型记录 ID**，不是上游模型标识。

### MCP 管理

HTTP MCP 请求（包括旧式 SSE 返回的消息端点）必须与配置 URL 同源；禁止跨域跳转或 HTTPS 降级，避免认证头及工具参数泄露。

在 **系统管理 → AI 配置 → MCP 管理** 中新增、查询、编辑和删除服务，使用开关动态启停。配置存入 `kaguya_mcp_server` 表，启动时自动建表并恢复已启用服务；连接失败会显示异常状态，可手动重试。

- 支持 `stdio`（可执行文件、JSON 参数数组、环境变量和绝对工作目录）、`streamable-http` 和旧版 `sse`（URL 与 HTTP 请求头）。认证可通过 `Authorization` 等请求头配置；暂不提供 OAuth 登录流程。
- 新配置默认停用。启用时先连接并发现工具；运行中编辑会先验证新连接，成功后保存并替换，失败保留原配置。停用或删除会关闭连接，并取消正在执行的 MCP 请求。
- 启用工具对所有聊天生效，从下一轮请求注入；标题任务不使用 MCP。工具名称带唯一前缀，避免不同服务和内置工具重名。停用后，已有聊天轮次也不能继续调用旧连接；远端已产生的副作用不会撤销。
- 本地进程以 Kaguya 服务权限运行，继承服务环境，不读取交互式 shell 配置；工作目录不是沙箱。工具调用超时可设为 1–600 秒，连接与工具发现最多 15 秒，工具输出最多 64 KiB，每个服务最多 256 个工具。
- 当前接入 MCP tools；不加载 prompts/resources。复杂根级 schema（例如根级 `$ref`、`$defs` 或组合约束）暂不支持，发现时会明确报错。凭据随配置存储，列表不返回环境变量或请求头，编辑详情可读取原值。

### 3. 系统配置

![系统配置：对话与任务模型、User-Agent 和系统提示词](images/screenshots/settings.jpg)

- **默认对话模型**：请求没有显式选择模型时使用。
- **后台任务模型**：用于生成对话标题，可以与对话模型不同。
- **Agent Loop 最大步数**：默认 `0`（不限，与 pi 一致），也可设为 1–1000。达到手动设置的上限时，保存完整工具结果并暂停，可点击“继续执行”接着处理。已有配置保留原值，可在此改为 `0`。
- **会话压缩比例**：按模型最大上下文的百分比配置，默认 `90`，允许 `10–95`；新一轮聊天读取最新值，输入框的有效窗口显示同步使用该配置。保留剩余窗口用于输出，并遵守模型的输出上限。降低阈值会更早压缩，增加摘要成本；这不能消除模型幻觉，窗口未知时仍不自动压缩。
- **命令超时**：bash 默认及最大超时为 120 秒，可设为 1–86400 秒；单次工具参数只能缩短期限。MCP 继续使用各服务自己的超时。
- **全局 AGENTS.md 路径**：有序数组，默认 `~/.config/agents/AGENTS.md` 和 `~/.codex/AGENTS.md`；支持修改、追加及清空禁用。每个会话首次按服务进程用户读取，并自动加入所属项目根目录的 `AGENTS.md`；文件名不区分大小写，支持 `agents.md`、`Agents.md` 等变体。多个大小写变体同时存在时按文件名排序全部读取，同一实际文件去重；先全局、后项目。缺失的全局文件或项目根目录没有指令文件时跳过；项目文件通过 `os.Root` 读取，拒绝符号链接逃逸。全局与项目内容合计最多 256 KiB，非普通文件、无效 UTF-8、读取失败或超限会报错。快照随首个成功轮次保存到会话，后续请求复用，不重复读文件、不在历史中逐轮追加；服务重启和上下文压缩也不会丢失快照。空快照同样保留；首轮失败或取消不会落库，重试时重新读取。升级前没有快照的旧会话，在下一次成功续聊时补存一次。修改文件或路径仅影响新会话；普通系统配置仍按轮次读取。每次模型请求仍需携带这份系统指令，减少文件读取不等于免除模型输入 Token；标题任务不使用它。子目录内另有作用范围的指令，仍由 Agent 在处理相应文件前查看。
- **User-Agent**：用于服务端发起的聊天与标题生成请求。
- **系统提示词**：保留只读的基础人设，自定义内容追加在其后用于聊天；自定义内容留空时仍保留基础人设。

提供商、模型与系统配置均保存在数据库中。应用启动时初始化系统配置，但不会预选对话模型或任务模型。

### 4. Token 用量分析

![Token 用量概览与每日活动热力图](images/screenshots/usage.jpg)

选择日期范围后，可以按日、周、月查看活动。概览卡片展示累计 Token、日峰值 Token、去重后的活跃会话数，以及日峰值会话数。活动日期按 **UTC** 统计。

![按模型展示的 Token 构成：输入、输出、思考和缓存](images/screenshots/usage-composition.jpg)

构成图展示用量最高的 **前 6 个** 模型或提供商。输入包含缓存写入，输出不包含思考，缓存读取和思考单独展示，避免重复计数。

分析仅统计 **成功保存的聊天轮次**，包含已删除会话的历史消耗；不包含标题生成任务、失败调用和取消调用，因此不等同于上游的完整计费账单。

## 部署

### 原生二进制（推荐）

源码构建需要 Go（最低 1.26.8，以 `go.mod` 为准）、Bun、Make、Git、C 编译器、Tcl、curl、pkg-config 和 OpenSSL 开发文件（包含**静态 `libcrypto.a`**）。macOS 需安装 Xcode Command Line Tools，并执行 `brew install openssl@3 pkgconf tcl-tk`；Debian/Ubuntu 对应原生依赖为 `build-essential tcl pkg-config libssl-dev curl`。确保 `pkg-config --exists libcrypto` 成功，必要时设置 `PKG_CONFIG_PATH`。

```sh
git clone https://github.com/lyonmu/kaguya.git
cd kaguya
CGO_ENABLED=1 make build
./target/kaguya
```

全新安装首次运行时，程序使用 Go 的 `crypto/rand` 自动创建 `~/.kaguya/kaguya.key`，运行时不需要 `openssl` 命令或 shell。**请安全备份生成的密钥，切勿用新生成的密钥覆盖旧密钥。** `make install` 构建并安装到 `~/.local/bin/<仓库目录名>`；确保 `~/.local/bin` 已存在并加入 `PATH`。请以普通用户运行，不要以 root 运行。预构建二进制启动无需 Go/Bun、Docker 或数据库服务；编码工具仍需要主机 Bash 和项目工具链，HTTPS 模型请求需要可信 CA 证书。

`make native` 下载官方 [SQLCipher v4.19.0](https://github.com/sqlcipher/sqlcipher/releases/tag/v4.19.0) 源码，校验固定 SHA-256，并构建到 `target/sqlcipher`。Go `database/sql` 适配器使用 `github.com/mattn/go-sqlite3`，通过 `USE_LIBSQLITE3` 禁用它自带的明文 SQLite 源码。SQLCipher 和 OpenSSL 以明确的**静态库**链接：应用运行时不需要 SQLCipher/OpenSSL 动态库，但仍使用平台系统库（如 Linux 的 libc）。前端继续内嵌。请在目标系统／架构上构建；当前构建不支持 `CGO_ENABLED=0`，也不支持仅修改 GOOS/GOARCH 的交叉编译。缺失构建依赖时明确失败。分发二进制时需保留 SQLCipher、OpenSSL 和适配器的许可证声明；SQLCipher 声明会复制到 `target/sqlcipher`。

**可写数据不存放在 `go:embed` 中**。启动时先检查或初始化默认密钥文件，再自动创建 `~/.kaguya/kaguya.db` 并迁移 schema；`~` 指的是**服务运行用户**的主目录。新建目录权限为 `0700`，新建数据库文件为 `0600`，不修改已有权限。

| 参数 | 环境变量 | 默认值 |
| --- | --- | --- |
| `--db.kind` | `DB_KIND` | `sqlite`（当前唯一启用的启动后端） |
| `--db.path` | `DB_PATH` | `~/.kaguya/kaguya.db` |
| `--db.key-file` | `DB_KEY_FILE` | 未设置时初始化／使用 `~/.kaguya/kaguya.key`；显式路径必须已存在 |
| `--host` | — | `127.0.0.1` |
| `--prepare-tls` | — | `false` |
| `--renew-tls` | — | `false` |
| `--trusted-host` | — | 空；额外信任的精确域名 |
| `--port` | — | `9024` |
| `--router-prefix` | — | `/kaguya/api` |

服务默认绑定 `127.0.0.1:9024`，仅允许本机连接。需要远程访问时，显式运行 `./target/kaguya --host=0.0.0.0`；IPv6 本机访问可使用 `--host=::1`。`--host` 只接受 IP 地址，空值或无效地址会被拒绝。

HTTP 和 WebSocket 拒绝跨域浏览器请求。默认仅接受 `localhost` 和 IP 地址作为请求 Host，以防 DNS 重绑定；通过自有域名的认证反向代理访问时，添加 `--trusted-host=agent.example.com`，代理须保留原始 Host、Origin 和 Sec-Fetch-Site，不要将任意外部 Host 重写为可信本机地址。Vite 开发代理已保留匹配的 Host/Origin。此校验不替代登录或网络访问控制。HTTP 请求体上限为 1 MiB；请求头读取上限 10 秒、请求读取上限 30 秒，SSE 回答不设短写入超时。

```sh
./target/kaguya --db.path=/srv/kaguya/kaguya.db --db.key-file=/secure/kaguya.key
# 引号中的 ~/ 路径也会由应用展开。
DB_PATH='~/.kaguya/kaguya.db' DB_KEY_FILE='~/.kaguya/kaguya.key' ./target/kaguya
```

父目录自动创建，相对路径基于工作目录解析，不支持 `~user` 展开。主程序**不加载 `config.yml`**；提供商、模型与提示词通过控制台配置并保存到 SQLite。

**SQLCipher 运行策略：** 保留 `sqlite` 配置值和 Ent dialect，实际引擎是 SQLCipher 4，不是明文 SQLite。应用在打开业务数据库前验证 `cipher_version`，迁移前读取 schema 校验密钥。每个物理连接在打开数据库时应用密钥，早于 WAL 等 PRAGMA。启动时启用并检查 WAL，每个连接设置 5 秒锁等待超时及 `synchronous=FULL`。应用连接池限制为一个连接，串行处理进程内数据库操作。SQLite 仍只允许一个写事务，适合个人／单实例服务；数据库应放在本地文件系统，不要使用共享网络文件系统。数据库旁可能生成 `kaguya.db-wal` 和 `kaguya.db-shm`。已有 MySQL/PostgreSQL 数据不会自动迁移；修改路径会创建或打开另一份数据库。

**TLS 1.3 与证书：** 数据库迁移及基础配置初始化后，程序读取 `kaguya_system_info` 中的证书和私钥，再启动 HTTPS。首次缺失时生成独立的 ECDSA P-256 自签名证书，有效期一年，包含 `localhost`、`127.0.0.1`、`::1`，以及明确配置的监听 IP 和可信域名。仅支持 TLS 1.3，不开放明文 HTTP 或降级入口。证书和私钥保存在 SQLCipher 加密数据库；查询只返回公钥证书、指纹、适用地址和到期时间，不返回私钥。

系统配置中的 **HTTPS / TLS 1.3** 可下载公钥证书、重新自签或导入匹配的 PEM 证书链与私钥；保存后必须重启，现有连接不热切换。证书域名不会自动加入 Host 白名单，域名访问仍需 `--trusted-host`。自签证书不会自动获得浏览器信任，需在访问设备上核对 SHA-256 指纹并导入信任。不要关闭证书验证。首次准备证书、导出公钥且保持服务关闭可运行：

```sh
./target/kaguya --prepare-tls --log.console-enabled=false > ~/.kaguya/kaguya.crt
openssl x509 -in ~/.kaguya/kaguya.crt -noout -fingerprint -sha256
```

此命令使用与正常启动相同的数据库参数，只初始化并退出，不启动监听或 MCP。已有证书会复用；到期、损坏或不匹配时启动明确失败，不自动更换身份。需要离线恢复时显式加 `--renew-tls` 重新自签，之后重新分发并信任公钥证书。备份数据库时也保留其中的 TLS 身份；TLS 私钥与 SQLCipher 密钥是两种不同的密钥。

macOS 若出现 `remote error: tls: unknown certificate`，通常是访问客户端拒绝了自签名证书。核对导出文件的指纹后，可将它加入当前用户登录钥匙串的 SSL 信任：

```bash
security add-trusted-cert -r trustRoot -p ssl -k "$HOME/Library/Keychains/login.keychain-db" "$HOME/.kaguya/kaguya.crt"
security verify-cert -c "$HOME/.kaguya/kaguya.crt" -p ssl -s localhost
```

随后重新打开浏览器连接。其他设备须分别配置证书信任；更换服务端证书后也需更新信任和开发代理使用的证书文件。

**密钥管理：** 密钥必须是普通文件，内容为恰好 64 个十六进制字符（32 字节密码学随机数据），末尾可带 LF／CRLF。Unix 下拒绝组用户／其他用户可访问的密钥文件，请使用 `chmod 600`。它是 AES-256 原始密钥，**不是口令或 TLS 证书**。程序没有内置／默认秘密，也不会回退到未加密存储。启动日志之后、初始化数据库之前，由 `internal/init/sqlcipher.go` 检查密钥：未设置 `--db.key-file`／`DB_KEY_FILE`，且默认密钥及配置的数据库文件均不存在时，Go 生成 32 字节随机数据，写入私有临时文件并同步后原子发布，不覆盖已有文件。已有密钥校验后复用；密钥缺失但数据库文件（包括空文件）、WAL、SHM 或回滚日志已存在时，明确报错并要求恢复原密钥。显式指定的密钥路径必须已存在，即使指定的是默认位置。无效密钥不会被替换；错误密钥或明文数据库会打开失败，不自动转换。密钥内容不作为 CLI 参数、序列化配置或应用日志字段；内部 DSN 含密钥，禁止记录。`modernc.org/sqlite` 仅作为加密测试的独立明文引擎保留，应用不再使用它。

**已有数据库：** 给明文 SQLite 配置密钥并不能直接加密旧文件。先停止并备份旧部署，再通过 SQLCipher 的带密钥 `ATTACH` 和 `sqlcipher_export()` 显式迁移到**新文件**，参考[官方转换说明](https://discuss.zetetic.net/t/how-to-encrypt-a-plaintext-sqlite-database-to-use-sqlcipher-and-avoid-file-is-encrypted-or-is-not-a-database-errors/868)。切换 `--db.path` 前，核对 schema、业务数据、时间字段和使用目标密钥重新打开的结果。MySQL/PostgreSQL 数据迁移需另行处理。本次不实现明文自动迁移、密钥轮换或旧版 SQLCipher 格式转换。

**备份与升级：** 最简单的备份方式是先停止服务，将数据库及仍存在的 WAL/SHM 文件作为整体复制；写入期间不能只复制主数据库，也不要手动删除 WAL。密钥必须**单独、安全地备份**，丢失后无法恢复数据。在线导出应使用支持 SQLCipher 的工具，并为目标数据库显式设置密钥；不要假定普通 SQLite 备份或 `VACUUM INTO` 会生成加密备份。升级前备份数据和密钥、停止服务、替换二进制，再以相同运行用户、路径和密钥重启。前台日志由配置的 logger 输出；后台运行和启停管理交给服务管理器。

**安全边界：** SQLCipher 加密数据库页及 WAL 中的页内容，不加密所有文件系统元数据、日志、工具输出或进程内存数据。密钥与数据库放在同一磁盘相邻位置，不能防止两者一起被窃取；有需要时采用独立挂载的秘密文件、操作系统秘密配置和磁盘加密。运行中的 Agent Bash 工具具有服务用户权限，可能访问密钥；数据库加密不是工具沙箱。TLS 证书保护网络传输，不保护本地密钥。远程访问控制台时，请使用带访问控制的 HTTPS 反向代理；应用本身没有内置登录。调试模式也不记录含参数的数据库 SQL，避免提示词、API Key 和 MCP 凭据泄露到日志。

打开 [https://localhost:9024](https://localhost:9024)，添加提供商（完整请求 URL、API Key）及至少一个模型，然后在**系统配置**中选择默认对话模型；可选后台任务模型用于生成标题。

知识库与语义检索可通过自行配置的外部 MCP 工具提供，不要求本地 pgvector 服务。应用不内置知识库、长期记忆、Embedding 或 FTS5 搜索。

### 遗留 Docker/PostgreSQL 参考（已禁用）

以下仅记录**此前的部署方式**，不是当前二进制支持的启动方式。MySQL/PostgreSQL 启动分支已注释，驱动／辅助代码及现有 [Dockerfile](Dockerfile)、[docker-compose.yml](docker-compose.yml) 原样保留；其中 PostgreSQL 启动参数现在会明确报错。新安装请不要执行这些命令；恢复该部署前需要先重新启用并验证对应后端。不要删除已有 `pgvector` 数据。

<details>
<summary>历史部署说明</summary>

### 1. 准备环境

文档统一采用 **PostgreSQL + pgvector** 作为唯一数据库方案。当前对话、配置和用量记录存储在 PostgreSQL 中；知识库、长期记忆、Embedding 和向量检索属于后续扩展方向，部署 pgvector 镜像不会自动启用这些应用功能。

需要 **Docker**、**Docker Compose** 和 **Git**。Go 与 Bun 由镜像构建阶段提供，部署时无需在宿主机安装。

仓库中的 [docker-compose.yml](docker-compose.yml) 使用 **host 网络**：Kaguya 通过 `127.0.0.1:5432` 连接 PostgreSQL，界面监听 `9024` 端口。请使用支持 host 网络的 Docker 环境，并确保这些宿主机端口可用。该模式下服务直接使用宿主机端口，而不依赖 `ports` 映射。

```sh
git clone https://github.com/lyonmu/kaguya.git
cd kaguya
```

启动前，检查 Compose 文件中的数据库密码和数据目录。示例数据库凭据需与下文说明的应用连接参数保持一致。

### 2. 构建镜像并启动服务

```sh
docker build -t kaguya:latest .
docker compose up -d
docker compose ps
docker compose logs --tail=100 kaguya-svc
```

[Dockerfile](Dockerfile) 使用 Bun 构建前端、Go 1.27 构建后端，再将内嵌 Web 控制台、二进制和 CA 证书打包进 BusyBox 运行镜像。Compose 引用本地的 `kaguya:latest` 镜像，因此需要先构建镜像再启动服务。

| 服务 | 镜像 | 职责 |
| --- | --- | --- |
| `kaguya-svc` | `kaguya:latest` | Web 控制台与 API，端口为 `9024` |
| `pgvector-svc` | `pgvector/pgvector:pg18-trixie` | 带 pgvector 的 PostgreSQL，端口为 `5432` |

PostgreSQL 数据通过 Compose 的绑定挂载 `${PWD}/pgvector:/var/lib/postgresql` 持久化。请在仓库根目录运行 Compose，保持路径一致。应用会在需要时创建 `kaguya` 数据库，并在启动时迁移 schema；配置的数据库账号需要具备相应权限。

### 3. 配置首次对话

在部署宿主机上打开 [https://localhost:9024](https://localhost:9024)。远程访问需要显式设置 `--host=0.0.0.0`。首次聊天前：

1. 进入 **AI 提供商**，填写协议、完整请求 URL 和 API Key，新增提供商。
2. 使用提供商的上游模型标识，添加至少一个模型。
3. 进入 **系统配置**，选择默认对话模型，按需选择后台任务模型，然后保存。
4. 返回 **对话管理** 发送消息。也可以直接在输入框选择模型，而不设置全局默认模型。

### 4. 容器配置

启动配置通过容器命令参数传入。主程序 **不加载 `config.yml`**；提供商与提示词通过控制台配置。

下表描述当前 Docker 镜像使用的参数值，不是直接运行裸二进制时的默认值：

| 参数 | Docker 部署值 | 用途 |
| --- | --- | --- |
| `--port` | `9024` | HTTP 端口 |
| `--router-prefix` | `/kaguya/api` | API 路由前缀 |
| `--db.kind` | `postgresql` | 连接 pgvector 数据库所用的 PostgreSQL 驱动 |
| `--db.host` | `127.0.0.1` | host 网络下的数据库地址 |
| `--db.port` | `5432` | PostgreSQL 端口 |
| `--db.user` | `pgvector` | 数据库账号 |
| `--db.password` | `pgvector-123` | 示例密码，请替换为实际部署密码 |
| `--db.db_name` | `kaguya` | 应用数据库 |

修改这些值时，在 Compose 文件的 `kaguya-svc` 中设置 `command`。它会替换 Dockerfile 的 `CMD`，因此需要包含所需的完整应用参数，尤其是 `--db.kind=postgresql` 与数据库连接信息。通过 `docker run --rm kaguya:latest --help` 查看更多选项。

首次初始化数据库时，保持数据库服务的 `POSTGRES_USER`、`POSTGRES_PASSWORD` 与应用的 `--db.user`、`--db.password` 一致。Compose 中的 `POSTGRES_DB=postgres` 指定初始数据库，Kaguya 使用独立的 `kaguya` 数据库。修改初始化环境变量不会更新已有数据目录中的数据库凭据。

### 5. 日志、更新与停止

```sh
docker compose logs -f --tail=100 kaguya-svc pgvector-svc
docker compose stop
docker compose up -d
```

更新源码后，重新构建并重建应用容器：

```sh
docker build -t kaguya:latest .
docker compose up -d --no-deps kaguya-svc
```

`docker compose down` 移除容器，但保留绑定挂载的 PostgreSQL 数据。重新部署时请保留 `pgvector` 数据目录，并在升级前备份数据库。

</details>

## 架构

聊天使用 assistant-ui 的 ExternalStoreRuntime、消息视口和输入组件，沿用 Go SSE 与数据库历史；配置表单、表格和通用控件继续使用 Ant Design，用量图表继续使用 ECharts。会话列表、消息和输入区采用侧栏式聊天布局。并发状态保存在当前浏览器应用内，刷新或关闭页面会断开流式请求；这不是服务端离线任务队列。Go 按请求使用独立 goroutine，同一会话保持互斥，不同会话可并发等待模型和工具。实际吞吐仍受提供商限流、数据库和主机资源约束。

```text
浏览器：React 19 + TypeScript + assistant-ui + Ant Design + Tailwind CSS + ECharts
    │  SSE 对话 / JSON API
    ▼
Go 二进制：Kong CLI → Gin 路由 → 应用服务
    ├── Agent Runtime（charm.land/fantasy）→ 已配置的模型提供商
    ├── 对话轮次、内容块与模型上下文 → Ent → SQLCipher 加密 SQLite（WAL）
    ├── 提供商／模型配置、系统配置与用量查询 → Ent
    └── 内嵌前端 / Swagger / Prometheus
```

| 路径 | 职责 |
| --- | --- |
| `main.go`、`internal/cmd/`、`internal/config/` | CLI 解析、启动流程与基础设施配置 |
| `internal/router/`、`internal/api/` | HTTP 路由与请求响应处理 |
| `internal/service/agent/` | 流式对话、历史持久化、上下文和标题生成 |
| `internal/agent/runtime/`、`internal/agent/token/` | 模型适配、执行与用量记录抽象 |
| `internal/agent/tools/` | pi 风格的七个工具、目录边界、输出截断、文件修改与命令执行 |
| `internal/service/system/` | 提供商／模型管理、系统配置与用量分析 |
| `internal/ent/schema/` | 手写数据库 schema，其余 Ent 文件由工具生成 |
| `web/` | Web 控制台源码与前端测试 |
| `docs/` | 生成的 Swagger 文档 |

### 编码工具

参照 [pi 的工具设计](https://github.com/earendil-works/pi/tree/acaa253cc8e3f159e6100b6f3874861b1f0bfc99/packages/coding-agent/src/core/tools) 实现七个 Go 工具，不包含 PowerShell：

| 工具 | 行为 |
| --- | --- |
| `read` | 文本分页读取或图片附件；文本最多 2000 行 / 50KB，返回续读位置 |
| `bash` | 在项目目录执行命令，合并 stdout/stderr，保留末尾 2000 行 / 50KB；可指定超时，取消时终止进程组 |
| `edit` | 同一原始文件上的多处唯一、不重叠替换，全部校验通过后原子写入；保留 BOM/换行符，返回 diff |
| `write` | 创建或覆盖文件，自动创建父目录，原子替换 |
| `grep` | 使用 `rg` 搜索，支持正则/字面量、大小写、glob、上下文，默认最多 100 个匹配 |
| `find` | 使用 `fd` 按 glob 查找，遵循忽略规则，默认最多 1000 项 |
| `ls` | 按名称列出文件夹，包含隐藏文件，目录带 `/`，默认最多 500 项 |

`tools.New(workspace, global.Logger)` 封装一组工具并提供 `CodingTools()`（前四个）、`ReadOnlyTools()`（read/grep/find/ls）和 `AllTools()`。项目聊天通过现有 `WithTools` 注册默认四个；查询工具作为可选工厂保留，不额外扩大默认模型工具清单。续聊从数据库恢复项目归属，不能通过请求的 `project_id` 改变目录。每轮默认不限模型步骤，可在系统配置设置上限；目录失效时明确报错，不退回主机工作目录。

工具输入通过 JSON Schema 传入模型：Go 类型定义字段类型、必填项和说明，工具契约补充行号/数量/超时范围、非空字符串及 `edits` 数组约束。各工具描述包含正确的 JSON 参数示例、用途边界和失败后的修正方法。本地执行前复用预编译 Schema 校验，并额外拒绝顶层未知字段；嵌套 `edits` 对象的未知字段约束也会发送给模型。校验失败不执行工具，返回具体字段错误。Fantasy 当前只重建根级 `properties/required`，因此不宣称所有提供商都支持相同的严格生成模式。MCP 工具保留远端公布的描述和 Schema，不套用内置文件工具参数。

工具日志和历史中的 `call_id` / `tool_call_id` 是上游协议的调用关联标识，必须原样返回以对应工具结果；其格式可包含 UUID。会话、轮次和内容块的数据库主键仍使用本地雪花 ID，二者不互相替换。

服务端适配：文件操作使用 `os.Root` 限定项目范围，`write/edit` 不接受符号链接路径；同路径修改在进程内串行。文本/编辑/写入有 32MB 安全上限，超大文件请用 bash 分段处理；图片附件上限 10MB，PNG/JPEG/GIF 超过 2000 像素时缩小，WebP/BMP 原样传递。编辑支持 pi 的 Unicode/尾部空白匹配，保留未修改行；过大的 diff 会截断且不返回不完整的 patch。命令完整输出临时保存在 `/tmp/kaguya/YYYYMMDD/<会话ID>/bash-<雪花ID>.log`，可用 `read` 分页读取；工作区外只允许读取当前会话生成的输出路径。Kaguya 不删除这些文件，其生命周期交由操作系统的临时目录机制管理。当前前端沿用工具调用开始/结束与最终结果展示，不逐块推送 bash 输出。

**权限警告：工作目录不是沙箱。** bash 以服务进程权限运行，可访问该用户能够访问的主机资源；文件工具的路径限制不约束 shell 命令。仅对可信用户开放服务，建议通过低权限用户或容器限制权限，不要将可执行工具的 API 直接暴露到公网。文件修改和命令副作用立即生效，即使对话失败、取消或历史未保存也不会回滚。日志记录工具名、调用 ID、项目/对话与耗时，不记录原始命令或文件内容。

编码 Agent 按原生主机服务使用和验证：运行 `make install` 安装二进制，再使用默认 SQLite 数据库或显式指定 `--db.path` 启动服务。本机需要安装 Bash；启用可选搜索工具时还需 `rg` 和 `fd`，缺失时明确报错，不自动下载。项目所需的 Git、Go、Bun 等命令也应安装在主机，并出现在服务进程的 `PATH` 中；系统服务的环境可能与交互式终端不同。本次工具集不以 Docker 运行为目标，未调整已有 Docker 配置。

## API

使用默认路由前缀时，常用端点如下：

| 端点 | 用途 |
| --- | --- |
| `POST /kaguya/api/v1/chat/sse` | SSE 流式对话 |
| `GET /kaguya/api/v1/chat/ws` | WebSocket 对话 |
| `GET /kaguya/api/v1/chat/conversation/page` | 分页查询对话列表 |
| `GET /kaguya/api/v1/chat/conversation/:id/turns` | 查询对话轮次 |
| `GET /kaguya/api/v1/chat/conversation/:id/turns/:turn/blocks/:sequence` | 按需读取单个历史内容块 |
| `GET /kaguya/api/v1/chat/conversation/:id/context` | 查询对话上下文统计 |
| `GET /kaguya/api/v1/project/page` | 项目列表（名称前缀与分页） |
| `GET /kaguya/api/v1/project/directories` | 浏览服务端运行用户主目录内的文件夹 |
| `POST /kaguya/api/v1/project` | 创建项目 |
| `GET / PUT / DELETE /kaguya/api/v1/project/:id` | 项目详情、更新与删除 |
| `GET /kaguya/api/v1/system/mcp/page` | MCP 分页列表与运行状态 |
| `POST /kaguya/api/v1/system/mcp` | 创建 MCP 配置（默认停用） |
| `GET / PUT / DELETE /kaguya/api/v1/system/mcp/:id` | MCP 详情、修改、删除 |
| `PUT /kaguya/api/v1/system/mcp/:id/state` | 动态启停，请求体为 `{"enabled": true/false}` |
| `GET /kaguya/api/v1/system/provider/page` | 查询提供商列表 |
| `GET /kaguya/api/v1/system/model/page` | 查询模型列表 |
| `GET /kaguya/api/v1/system/usage` | Token 用量分析 |
| `GET /kaguya/api/v1/system/info` | 查询系统配置（`PUT` 用于更新） |
| `/kaguya/api/swagger/index.html` | Swagger UI |
| `/kaguya/api/metrics` | Prometheus 指标 |

对话响应包含 `is_project` 标记，由 `project_id` 是否为空派生，无需数据库回填。列表默认仅返回普通对话；`is_project=true` 仅查询项目对话，`project_id` 可指定项目（单独传入时兼容按项目查询），不能与 `is_project=false` 同时使用。筛选在数据库分页和计数前执行。SSE/WS 新对话通过 `project_id` 指定项目，续聊保留原有归属。

配置默认模型后，可以这样开启对话：

```sh
curl --cacert ~/.kaguya/kaguya.crt -N https://localhost:9024/kaguya/api/v1/chat/sse \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"flag":"chat","messages":"Hello, Kaguya"}'
```

在后续请求体中复用返回的会话 `id`，即可继续该会话的历史。显式选择模型时，传入值为本地模型记录 ID 的 `model_id`。流式帧使用 `start`、`delta`、`done` 和 `error`，完整轮次保存成功后才会发送 `done`。当前接口契约可查阅 [路由定义](internal/router/v1/) 与 [聊天 DTO](internal/dto/chat/)。

## 开发

源码开发需要 Go（最低 1.26.8，以 `go.mod` 为准）、Bun、Make 及上文列出的原生依赖。执行 `CGO_ENABLED=1 make build` 后通过 `./target/kaguya` 运行（全新安装自动初始化默认密钥），无需 Docker／数据库服务。请使用 Make 目标而非直接 `go build`／`go test`，以应用 SQLCipher 链接参数。局部测试可先执行 `make native`，再执行 `CGO_ENABLED=1 bash scripts/go-sqlcipher.sh test -race -count=1 ./internal/db ./internal/config`。

在仓库根目录执行 `CGO_ENABLED=1 make install`，会完整构建前后端并将二进制安装到 `~/.local/bin/<仓库目录名>`（通常为 `~/.local/bin/kaguya`），权限为 `0755`。请先创建 `~/.local/bin` 并加入 `PATH`；该命令不会启动服务。

```sh
CGO_ENABLED=1 make test    # 使用 SQLCipher 的 Go 测试，开启竞态检测
cd web
bun install --frozen-lockfile
bun run test               # 前端测试
bun run lint               # oxlint
bun run build              # TypeScript 检查与 Vite 生产构建
bun run dev # 默认信任 ~/.kaguya/kaguya.crt
```

开发前端时，保持原生应用运行在 `9024` 端口；Vite 将 `/kaguya/api` 代理到 `https://localhost:9024`，默认读取 `~/.kaguya/kaguya.crt` 信任本机公钥证书，可用 `KAGUYA_CA_CERT` 指定其他位置，保留证书验证。文件缺失时明确报错，请先运行上述 `--prepare-tls` 命令导出；生产构建不需要此文件。Vite 页面只用于本机开发；正常使用请打开后端内嵌的 HTTPS 页面。如果调整 API 前缀或部署地址，请同步前端 `VITE_API_BASE_URL` 与代理配置。修改后端后，执行 `make build` 并重启二进制。

修改 Ent schema 后，在仓库根目录执行 `go generate ./internal/ent`，保持生成的 Ent 代码与 schema 同步。

## 当前范围

- 控制台当前为中文界面，英文文档不代表已提供英文 UI。
- 聊天输入为文本；项目工具可以读取工作区图片并回传模型，但不等于浏览器图片上传。模型须支持工具调用，图片内容还需要视觉能力。
- 已有访问日志代码和 API，但访问日志中间件当前未启用，导航中也未开放该页面。
- 当前路由未提供内置用户登录和按用户隔离访问的能力。项目是实验性控制台，并非完整的多租户服务。

## 为什么叫 Kaguya？

**Kaguya** 源自 **Kaguya-hime / 辉夜姬**，即静谧、优雅而神秘的月宫公主。在《龙族》中，这个名字也与日本分部的超级人工智能系统相关联。项目借用这一形象，探索一个冷静、理性、可控的 Agent 核心。

## 许可证

[MIT](LICENSE)。

### 聊天交互

`mermaid` 代码块在所在文本块输出完成后由 Ant Design X Mermaid 直接渲染成图，首轮回答与已保存历史均支持；流式输出期间保留可读源码，不显示额外的图表生成占位动作。完成后可缩放、平移、下载，并可在图形与可复制源码之间切换；语法错误时显示错误及源码。渲染在浏览器本地完成。当前不承载 MCP Apps 或 Excalidraw 小组件，工具返回“Diagram displayed”或 checkpoint ID 本身不会显示图片；聊天提示词会告知模型在适用时直接在回答中提供 Mermaid 图。

点击进行中或最近会话时，会同步切换对话／项目列表、选中所属项目，并将当前条目滚动到可见位置。尚未保存的会话以及当前分页之外的选中条目也能在列表中直接访问。

项目对话输入框支持输入 `@` 搜索项目文件，使用 ↑／↓ 选择、Enter 添加、Esc 关闭。选中的文件以输入区域内可删除的 `@path/to/my file.go` 引用卡片显示，不在消息文本中插入带双引号的路径；文件列表通过独立请求字段发送，因此带空格的文件名无需转义。每次最多引用 8 个文件，发送时后端在项目工作区内读取并附上文本内容，普通对话不读取主机文件。单文件沿用 read 工具的 2000 行／50 KiB 限制，引用总量最多 256 KiB；不读取工作区外的路径或图片。文件搜索忽略常见依赖和构建目录，最多扫描 20000 个条目、返回 50 个匹配项。

思考过程与工具调用使用独立的可展开卡片，展示工具名称、关键参数、执行状态、耗时及输出。Markdown 支持表格、代码语言与按需语法高亮，代码块右上角提供复制按钮和成功／失败反馈；长代码与宽表格可独立滚动。明暗主题分别适配，鼠标操作避免重复焦点框，键盘操作保留焦点提示。
