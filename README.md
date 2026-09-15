<p align="center">
  <img src="images/kaguya.png" alt="Kaguya" width="120" />
</p>
<p align="center">A personal desktop AI Agent for everyday conversations and local projects</p>
<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh.md">简体中文</a>
</p>

# Kaguya

**Kaguya** is my personal desktop AI Agent, bringing everyday conversations, local project work, model and MCP configuration, history, and token usage into one application. The backend uses Go, the React interface ships with the application, and data is stored locally in a SQLCipher-encrypted database.

The project is oriented toward a cross-platform Desktop application. **The native desktop host and application packaging currently implemented in this repository support macOS 14 and later**; the desktop instructions below cover the macOS application. The interface is currently in Chinese.

## Getting started

1. Open `Kaguya.app`. When installing from a DMG, drag the application into `Applications`, then open it.
2. Click **System management** (系统管理) at the bottom left. Under **AI configuration → Providers and models** (AI 配置 → 提供商与模型), search the **Provider catalog** (提供商目录, models.dev api.json) to prefill the name and base URL, then enter the API key; you can also type a custom name, base URL, and API key manually.
3. In **System configuration**, synchronize the [models.dev](https://models.dev) catalogs manually or on a schedule, or press Sync directly under **AI configuration → Provider catalog / Model catalog**. The provider catalog comes from `api.json` and keeps only providers with an API address, sorted A→Z by name; the model catalog comes from `models.json`, is deduplicated by model, and sorts by release date newest first. Open a provider's **Model management** and select a catalog model to fill its identifier, limits, and capabilities; the request protocol and path are prefilled from protocol defaults and stay editable for provider-specific differences.
4. Select a default chat model in **System configuration** (系统配置). Configure a background-task model to generate conversation titles automatically.
5. Return to **Conversation management** (对话管理) to chat, or switch to **Projects** (项目) and add a local directory for project work.

An installed application needs neither Go, Bun, nor a separate database service. Model requests use your configured providers and credentials; project commands and local MCP services use tools installed on the host.

## Features and usage

These screenshots were captured from the locally installed `Kaguya.app` on **2026-09-13**. Providers, models, MCP services, settings, and usage belong to this instance; **they are not bundled defaults or guarantees of upstream capabilities**. For example, the screenshots show a 128-step limit and 50% compaction threshold; product defaults remain those in the settings table. The provider list is filtered by name, with API keys kept masked.

### Conversations and history

![Desktop chat home with history, model selection, and composer](images/screenshots/desktop-2026-09-13/chat.jpg)

- **Streaming responses**: Markdown, tables, highlighted code, and copying; provider-reported reasoning, tool arguments, and results can be expanded independently.
- **Mermaid diagrams**: Mermaid blocks render when their generation finishes, with zoom, pan, download, and source switching. Markdown images load after a user click.
- **Model selection**: choose a provider and then a model in the composer. The collapsed control shows the model name; requests without an explicit choice use the system default chat model.
- **Concurrent conversations**: running conversations continue while switching chats or visiting system pages. Inspect running conversations and stop them separately. Each conversation runs one turn at a time.
- **Conversation management**: create, search by title prefix, continue, rename, and delete conversations. Reload or browse paginated history and inspect tokens, duration, and tool-call counts for each turn.
- **Context management**: the composer shows context occupancy from the latest model call. At the configured threshold, earlier content is summarized while recent messages and original history are retained. The summary is saved with the successful turn and reused on continuation. Unknown model windows show unknown occupancy and skip automatic compaction.

**Enter** sends; **Shift + Enter** inserts a newline. Stop an active response with the stop control. Stopped, failed, or disconnected turns retain their generated content and tool records. A continuation retains user questions from unfinished turns; partial assistant responses and tool records are display-only. Closing the application, refreshing the page, or losing the streaming connection interrupts the current turn.

### Local project workspaces

![Saved conversation in a personal example project, with reasoning and tool execution cards](images/screenshots/desktop-2026-09-13/project-chat.jpg)

Under **Projects**, search projects by name prefix and choose an existing directory inside the current user's home. Edit its name, path, and description, and create multiple conversations within it. Ordinary and project conversations have separate lists. Project paths must be unique. Deleting a project preserves its conversations as ordinary conversations and leaves host files intact.

Project conversations enable four built-in tools, using the project directory as their working directory:

| Tool | Purpose |
| --- | --- |
| `read` | Read paginated text or images the model can process |
| `bash` | Execute host commands with combined stdout/stderr, timeouts, and cancellation |
| `edit` | Locate and replace file content, returning a diff |
| `write` | Create or overwrite files and create parent directories as needed |

Type **@** in the composer to search project files, use ↑/↓ to select, Enter to add, and Esc to dismiss. Files appear as removable reference cards; their text is read when sending. Each message supports up to **8** files, **2000 lines / 50 KiB** per file, and **256 KiB** in total.

![Project file reference search](images/screenshots/desktop-2026-09-13/file-reference.jpg)

The project conversation header menu offers **View code and changes** (查看代码与改动): a file tree, code preview, and uncommitted Git changes. Diffs support unified and split views. The tree follows `.gitignore` / `.dockerignore` and skips common dependency directories. Text previews are limited to **512 KiB**, and individual file diffs to **1 MiB**. Non-Git directories also support file browsing.

![Project code browser with file tree and split diff](images/screenshots/desktop-2026-09-13/code-split.jpg)

<details>
<summary>Show unified diff and file preview</summary>

![Unified diff view](images/screenshots/desktop-2026-09-13/code-unified.jpg)

![File content preview](images/screenshots/desktop-2026-09-13/code-file.jpg)

</details>

File tools use `os.Root` to constrain project paths. **Bash and local MCP processes run with the current user's permissions; a working directory is not a sandbox.** File changes and external operations take effect immediately; stopping a conversation does not undo them. Long command output is truncated for display, with oversized output saved under `/tmp/kaguya/YYYYMMDD/<conversation-id>/` for paginated reads by that conversation. Cleanup follows the operating system's temporary-directory lifecycle.

### Providers and models

Open **System management → AI configuration** (系统管理 → AI 配置), which holds the **Providers and models** (提供商与模型), **Provider catalog** (提供商目录), and **Model catalog** (模型目录) tabs.

- **Providers and models**: create and edit providers and models; a provider stores only its name, base URL, and API key, and new providers can be prefilled by searching the provider catalog.
- **Provider catalog**: models.dev provider catalog (api.json) sorted A→Z by name, searchable by name, identifier, package, or API address; new providers are added by searching from **Providers and models**.
- **Model catalog**: models.dev model catalog (models.json), deduplicated by model and sorted by release date newest first, searchable by name, identifier, provider, family, or description.

![Provider list filtered by name, with API keys masked](images/screenshots/desktop-2026-09-13/providers.jpg)

![Provider model management drawer](images/screenshots/desktop-2026-09-13/models.jpg)

The protocol lives on the **model**, so one provider can serve several protocols; the request protocol decides which request implementation runs:

| Model protocol | Configuration value | Default request path | Example final request URL |
| --- | --- | --- | --- |
| Chat | `openai-chat` | `/chat/completions` | `https://api.example.com/v1/chat/completions` |
| Response | `openai-response` | `/responses` | `https://api.example.com/v1/responses` |
| Message | `anthropic` | `/messages` | `https://api.example.com/v1/messages` |

A provider base URL is stored as a plain string (usually taken directly from the provider catalog) and the final request URL is the base URL plus the model request path. The request path defaults to the protocol suffix constant (`/chat/completions`, `/responses`, or `/messages`) and can be replaced with any full path, for example DeepSeek's Anthropic-compatible endpoint as `/anthropic/v1/messages`. Switching protocols fills the default path. Provider types include standard `normal` and `opencode-go`, which adds an OpenCode session header.

Model configuration includes display name, upstream model identifier, request protocol, request path, reasoning level, context window, maximum output tokens, and Tool/Vision/JSON capability metadata. Selecting a model from the catalog fills the name, identifier, and capability metadata, and the request path comes from protocol defaults; the catalog does not guarantee that a configured provider exposes that model, so verify availability and adjust metadata when needed. Project and MCP tools require tool-calling support; reading images also requires vision support.

API keys are encrypted at rest and masked in lists; plaintext is returned only on explicit reveal. Leaving the key empty while editing preserves the existing value. Providers and models come from personal configuration; the application does not preselect a default chat or background-task model.

### MCP tools

Open **System management → AI configuration → MCP management** (系统管理 → AI 配置 → MCP 管理).

![MCP service transports, runtime status, tool counts, and enable switches](images/screenshots/desktop-2026-09-13/mcp.jpg)

Search, add, edit, and delete services, enable or disable them dynamically, and inspect connection status, tool counts, and errors.

| Transport | Configuration |
| --- | --- |
| `stdio` | Executable command, JSON argument array, environment variables, optional absolute working directory |
| `streamable-http` | Service URL and HTTP headers |
| `sse` | SSE URL and HTTP headers |

New services are disabled. Enabling connects and discovers tools; application restarts restore enabled services. Editing an enabled service validates the replacement connection first and preserves the existing configuration on failure. Enabled MCP tools are available to ordinary and project conversations from the next request; title tasks do not use tools. Disabling or deleting a service closes its connection and cancels ongoing MCP requests.

Tool timeouts range from **1–600 seconds**. HTTP authentication is configured through headers. Local services inherit the application process environment, with additional variables configurable per service.

### System configuration

Open **System management → System configuration** (系统管理 → 系统配置).

| Setting | Behavior |
| --- | --- |
| Default chat model | Used without a manual model choice |
| Background-task model | Generates conversation titles; may differ from the chat model |
| Agent Loop maximum steps | Defaults to `0` (unlimited); `1–1000` sets a limit, saves progress when reached, and allows continuation |
| Command timeout | Default and maximum Bash timeout: `120` seconds by default, range `1–86400`; the model may request less |
| Maximum chat request retries | Defaults to `5`, range `0–20`; retries transient errors such as rate limits and overload with backoff, but never after content has been emitted |
| Context compaction percentage | Defaults to `90%`, range `10–95%`; the rest of the window is reserved for output |
| Global AGENTS.md paths | Loads personal instructions in order; clear the list to disable global file loading |
| Catalog synchronization | Provider catalog defaults to `https://models.dev/api.json` and model catalog to `https://models.dev/models.json`, both complete HTTP(S) URLs; sync manually or enable an interval from `1–720` hours, and one sync updates both catalogs. **AI configuration → Provider catalog** sorts A→Z by name, while **Model catalog** supports searching by name, identifier, provider, family, or description and sorts by release date newest first |
| Global base prompt | Editable base persona used by new chat requests; it may be empty |
| Additional system prompt | Appended to the base prompt for chat |

Default global instruction paths are `~/.config/agents/AGENTS.md` and `~/.codex/AGENTS.md`. The project root's `AGENTS.md` is included automatically, with case-insensitive filenames. Instructions are saved as a snapshot with the conversation's first successful turn and reused across restarts and compaction. File or path changes apply to new conversations. Global and project instructions share a **256 KiB** limit.

Other saved settings apply to new requests. The interface also provides light/dark themes and collapsible sidebars.

### Token usage analytics

Open **System management → Usage analytics** (系统管理 → 用量分析).

![Token usage summary and activity heatmap for the last year](images/screenshots/desktop-2026-09-13/usage.jpg)

- The date range controls summary cards: total tokens, daily token peak, active conversations, and daily conversation peak.
- The activity heatmap always covers the last year, with daily, weekly, or monthly aggregation.
- Composition uses all history to show the top ten models or providers, separating input, output, reasoning, and cache reads.

![Token composition by model across all history](images/screenshots/desktop-2026-09-13/usage-composition.jpg)

Dates use **UTC**. Only successfully completed chat turns count, including historical consumption from deleted conversations. Context-summary usage counts toward chat turns; title tasks and unfinished turns are excluded. This page therefore reflects chat usage recorded by the application.

## Installation and building

### macOS desktop application

Source builds require the Go version in [go.mod](go.mod) (currently `1.26.8`), Bun, Make, Git, Xcode Command Line Tools, Tcl, pkg-config, curl, and Perl. On macOS, install additional dependencies with `brew install pkgconf tcl-tk`.

```sh
git clone https://github.com/lyonmu/kaguya.git
cd kaguya
make package-macos
# Open target/Kaguya.app, or copy it into Applications
```

`make package-macos` builds the frontend and backend and assembles `target/Kaguya.app`. `make dmg-macos` produces the drag-to-install `target/Kaguya-<version>.dmg`. Build natively on macOS for the target architecture; both C dependencies and the application default to a minimum system version of **14.0**.

```sh
make build                 # Build target/kaguya
./target/kaguya             # Open the desktop window
mkdir -p ~/.local/bin
make install               # Build and install the CLI entry at ~/.local/bin/kaguya
```

The binary name follows the repository directory name. `make native` downloads and verifies pinned OpenSSL / SQLCipher sources and builds static libraries; the application needs no separately installed copies of these shared libraries. CGO is required. Use Make targets to apply the SQLCipher link settings.

Distribution signing and notarization use the existing scripts, with the identity and keychain profile supplied by the release environment:

```sh
CODESIGN_IDENTITY='Developer ID Application: Name (TEAMID)' make sign-macos
CODESIGN_IDENTITY='Developer ID Application: Name (TEAMID)' \
  NOTARY_PROFILE=kaguya make notarize-macos
```

Ordinary packaging targets automatically apply an ad-hoc signature to the complete application bundle, without a certificate or Apple account; the DMG itself remains unsigned. This does not establish Apple developer trust. If the first launch is blocked after installation, colleagues who trust the source can allow it through System Settings → Privacy & Security → Open Anyway. An email address alone is not a signing identity; formal distribution still requires an Apple-issued Developer ID Application certificate. The notarization profile defaults to `kaguya` and its keychain credentials must be configured beforehand using `notarytool`; the notarization target handles both the application and DMG.

### Desktop runtime environment

The desktop window calls the shared Go services through Wails' native asset channel and **does not listen on a local network port**. External links open in the system browser and copying uses the system clipboard. Model and remote MCP requests still access the network as configured.

When launched from Finder, the application attempts to supplement `PATH` through a login shell so it can locate Homebrew, Bun, uvx, and other commands. It keeps the existing `PATH` if the lookup fails or times out after 5 seconds. This step imports only `PATH`. To inherit terminal proxy variables or other environment settings, run `/Applications/Kaguya.app/Contents/MacOS/kaguya` from a terminal. Bash, Git, and other project tools must be available on the host.

### Optional Web mode

```sh
./target/kaguya --web
```

Open [http://127.0.0.1:9024](http://127.0.0.1:9024). Web mode shares the interface, services, and data, primarily for browser access and frontend development. `--host`, `--port`, and `--trusted-host` apply only to this mode. Remote access should use an authenticated HTTPS gateway that preserves the original Host / Origin / Sec-Fetch-Site and allows the exact hostname through `--trusted-host`.

## Local data and backups

| Item | Default location / behavior |
| --- | --- |
| Database | `~/.kaguya/kaguya.db`, SQLCipher-encrypted SQLite in WAL mode |
| Database key | `~/.kaguya/kaguya.key`, generated on the first start of a fresh installation |
| Providers, models, MCP, system settings, and conversations | Stored in the database |
| Custom database location | `--db.path` / `DB_PATH` |
| Custom key file | `--db.key-file` / `DB_KEY_FILE`; the specified file must already exist |
| Independent API key encryption key | `--secret-key` / `KAGUYA_SECRET_KEY`; derived from the database key when unset |

The application bundle contains the program and static resources; upgrades do not place data inside `.app`. Startup parameters use CLI flags / environment variables, while models and prompts are configured in the interface.

For backups, quit the application, copy the database and any remaining WAL/SHM files, and store the key separately and securely. Retain an independent API key encryption key if configured. Losing the key makes the data unreadable. Never replace the key for an existing database or open the same database in Desktop and Web modes simultaneously. File encryption cannot replace host access protection when both the database and key are lost or stolen together.

API keys use AES-256-GCM (`enc:v2:`). Legacy credentials require the [offline migration tool](cmd/migrate-provider-secrets/) on a database copy; startup does not rewrite old data in place.

## Project structure and development

```text
Desktop window / optional Web interface
        │ JSON + POST SSE
        ▼
Go startup and routes → Application services → Fantasy Agent → Model providers
                              ├── Project file tools / MCP tools
                              └── Ent → Local SQLCipher database
```

| Path | Responsibility |
| --- | --- |
| `main.go`, `internal/cmd/`, `internal/config/` | CLI, runtime modes, shared initialization and shutdown |
| `internal/desktop/` | Native window, asset channel, system integration, startup environment |
| `internal/router/`, `internal/api/`, `internal/dto/` | Routes, request handling, data contracts |
| `internal/service/agent/` | Chat, history, titles, instruction snapshots, context compaction |
| `internal/service/project/` | Project management, file browsing, Git diffs |
| `internal/service/system/` | Providers, models, MCP configuration, settings, usage |
| `internal/agent/runtime/`, `internal/agent/token/` | Fantasy model execution and usage recording |
| `internal/agent/tools/`, `internal/agent/mcp/` | Built-in file/command tools and MCP connection lifecycle |
| `internal/db/`, `internal/secret/`, `internal/ent/schema/` | Database, credential encryption, handwritten schemas |
| `web/` | React / TypeScript interface and tests |
| `build/darwin/`, `scripts/` | Desktop resources, native dependencies, packaging scripts |
| `docs/` | Generated Swagger documentation |

```sh
make test                  # SQLCipher + Go race tests
cd web
bun install --frozen-lockfile
bun run test
bun run lint
bun run build
```

For frontend development, run `./target/kaguya --web` from the repository root, then `bun run dev` inside `web/`. Desktop loads embedded assets; rebuild and restart the application after frontend changes.

After changing schemas in `internal/ent/schema/`, run `go generate ./internal/ent`. See [internal/router/v1/](internal/router/v1/) and [internal/dto/](internal/dto/) for API routes and DTOs. In Web mode, Swagger defaults to `/kaguya/api/swagger/index.html` and Prometheus to `/kaguya/api/metrics`. See [AGENTS.md](AGENTS.md) for collaboration and validation conventions.

## Name and license

Kaguya takes its name from Kaguya-hime (辉夜姬), also echoing the Japanese branch's AI system in *Dragon Raja* (《龙族》).

[MIT](LICENSE).
