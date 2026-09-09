<p align="center">
  <img src="images/kaguya.png" alt="Kaguya" width="120" />
</p>
<p align="center">A Go-native AI Agent with a web console</p>
<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh.md">简体中文</a>
</p>

# Kaguya

**Kaguya** is an experimental AI Agent application built with Go. It brings streaming conversations, model configuration, persistent history, and token analytics into one web console. The React frontend is embedded in the Go binary, so one service serves both the UI and the API.

The project is intended for learning, personal use, and exploring Agent runtime design. The current console is in Chinese; both READMEs describe the same features and use the same screenshots.

![Conversation workspace with history, model selection, and Markdown responses](images/screenshots/chat.jpg)

## Features

| Area | What you can do |
| --- | --- |
| Streaming chat | Receive responses over SSE, view Markdown and provider-reported reasoning, stop generation, and choose a model for a request. A WebSocket API is also available. |
| Conversation management | Continue saved conversations, search by title prefix, rename, and delete conversations; organize conversations by project, browse paginated history and review turn summaries. |
| Providers and models | Manage providers and their models through the UI; configure full request URLs, API keys, protocol types, and model metadata. |
| System configuration | Choose separate default chat and background-task models, append a custom system prompt, and configure the upstream `User-Agent`. Saved settings apply to new requests without restarting. |
| Token analytics | Inspect total usage, daily peaks, active conversations, an activity heatmap, and token composition by model or provider. |
| Deployment and development | Deploy with Docker and Docker Compose, persist data in PostgreSQL with pgvector, and access Swagger and Prometheus endpoints. |

## A tour of the console

Screenshots were captured from a running instance on **2026-09-09**. Model names, metadata, conversations, and usage are examples from that instance, not bundled defaults or a guarantee of upstream model availability. The model screenshot is cropped to the management drawer to omit provider credentials and endpoint details.

### 1. Conversations

**Projects:** The sidebar shows multiple folder-style project headings with indented conversations and an active-conversation highlight. Each project menu offers a new conversation, edit, and delete. Names may repeat, but canonical absolute paths must be unique among non-deleted projects (including symlink aliases). A directory can be reused after deleting its project. Select an existing folder on the server, starting at the running user's `~/` (for example, `/root` or `/home/ubuntu`); files and folders outside this boundary cannot be selected, including symlinks escaping it. Navigating into a directory selects it, so confirming the dialog creates the project directly; directories without children do not show an empty-folder message. Each project can contain multiple conversations. Open a project before starting a new conversation to associate it after its first successfully completed turn. Existing conversations remain in **All**. Deleting a project only detaches its conversations; it never deletes conversation history or host files. Project folders are metadata only: they do not automatically enable agent filesystem tools. In Docker, these paths refer to the container user's home; mount host folders beneath it if needed.

Use the left sidebar to create or reopen a conversation, search by title prefix, or switch between **All** conversations and **Projects**. The conversation header provides rename and delete actions. The composer lets you select a provider/model or use the default model: **Enter** sends, **Shift + Enter** inserts a newline, and **Stop** cancels an active response.

Replies support Markdown, code blocks, and collapsible reasoning when supplied by the provider. Turn summaries show tokens, duration, and tool-call counts. When usage and model-window data are available, the composer shows cumulative tokens from all completed turns as a percentage of 90% of the most recent model’s context window. This is a cumulative-usage indicator, not actual context occupancy, and it does not automatically trim messages.

Only successfully completed and saved turns become reusable conversation history. Failed or canceled partial replies are not saved as completed turns. With a background-task model configured, the UI requests a short Chinese title based on the first successful question and answer; manual titles are preserved.

### 2. Providers and models

Open **System management → AI providers** (**系统管理 → AI 提供商**) to add a provider, then open its **Model management** (**模型管理**) drawer to add models.

![Model management drawer showing model identifiers, reasoning metadata, and context windows](images/screenshots/models.jpg)

Providers support three explicitly selected API protocols:

| Protocol | Configuration value | Example full request URL |
| --- | --- | --- |
| OpenAI Chat Completions | `openai-chat` | `https://api.example.com/v1/chat/completions` |
| OpenAI Responses | `openai-response` | `https://api.example.com/v1/responses` |
| Anthropic Messages | `anthropic` | `https://api.example.com/v1/messages` |

Replace the example host with your provider's actual endpoint. **The request URL must include the complete endpoint path**: Kaguya uses it as configured and does not append `/chat/completions`, `/responses`, or `/messages`. Provider types include standard (`normal`) and OpenCode Go (`opencode-go`); the latter adds the OpenCode session header.

Each model has a display name and an upstream model identifier, plus metadata such as reasoning level, context window, maximum output tokens, and Tool/Vision/JSON capabilities. These fields describe the model; they do not by themselves enable attachments, register tools, or guarantee that every parameter is forwarded to the upstream API. Chat API model selection uses the **local model record ID**, not the upstream model identifier.

### 3. System configuration

![System configuration with chat and task models, User-Agent, and system prompts](images/screenshots/settings.jpg)

- **Default chat model**: used when a request does not select a model explicitly.
- **Background-task model**: used for conversation-title generation; it can differ from the chat model.
- **User-Agent**: applied to server-side chat and title-generation requests.
- **System prompt**: the read-only base persona remains in place; custom text is appended for chat. Leaving custom text empty retains the base persona.

Provider, model, and system settings are stored in the database. The application initializes system settings on startup but does not preselect a chat or task model.

### 4. Token analytics

![Token usage overview and daily activity heatmap](images/screenshots/usage.jpg)

Choose a date range and switch activity aggregation between daily, weekly, and monthly views. Summary cards show total tokens, daily peak tokens, distinct active conversations, and the daily conversation peak. Activity dates use **UTC**.

![Token composition by model, including input, output, reasoning, and cache](images/screenshots/usage-composition.jpg)

The composition chart shows the **top six** models or providers by usage. Input includes cache writes; output excludes reasoning; cache reads and reasoning appear separately to avoid double counting.

Analytics count **successfully saved chat turns**, including those from deleted conversations. Title-generation tasks, failed calls, and canceled calls are excluded, so this view is not a complete upstream billing report.

## Deployment

### 1. Prepare the environment

The documented deployment uses **PostgreSQL with pgvector** as its single database stack. Current conversations, settings, and usage records are stored in PostgreSQL. Knowledge bases, long-term memory, embeddings, and vector retrieval are planned extensions; deploying the pgvector image does not enable these application features automatically.

Requirements: **Docker**, **Docker Compose**, and **Git**. Go and Bun are provided by the image build stages and are not needed on the host for deployment.

The checked-in [docker-compose.yml](docker-compose.yml) uses **host networking**: Kaguya connects to PostgreSQL at `127.0.0.1:5432`, and the UI listens on port `9024`. Use a Docker environment with host networking support and keep these host ports available. In this mode, the services use host ports directly rather than relying on the `ports` mappings.

```sh
git clone https://github.com/lyonmu/kaguya.git
cd kaguya
```

Before starting, review the database password and data directory in the Compose file. Its example database credentials must match the application connection arguments described below.

### 2. Build the image and start the services

```sh
docker build -t kaguya:latest .
docker compose up -d
docker compose ps
docker compose logs --tail=100 kaguya-svc
```

The [Dockerfile](Dockerfile) builds the frontend with Bun and the backend with Go 1.27, then packages the embedded web console, binary, and CA certificates in a BusyBox runtime. Compose references the local `kaguya:latest` image, so build it before starting the services.

| Service | Image | Role |
| --- | --- | --- |
| `kaguya-svc` | `kaguya:latest` | Web console and API on port `9024` |
| `pgvector-svc` | `pgvector/pgvector:pg18-trixie` | PostgreSQL with pgvector on port `5432` |

PostgreSQL data is persisted through the Compose bind mount `${PWD}/pgvector:/var/lib/postgresql`. Run Compose from the repository root to keep this path consistent. The application creates the `kaguya` database if needed and migrates its schema at startup; the configured database account must have the corresponding permissions.

### 3. Configure the first conversation

Open [http://localhost:9024](http://localhost:9024), or use the deployment host's address. Before the first chat:

1. Open **AI providers**, add a provider with its protocol, full request URL, and API key.
2. Add at least one model with the provider's upstream model identifier.
3. Open **System configuration**, select a default chat model, optionally select a background-task model, and save.
4. Return to **Conversations** and send a message. You can also choose a model in the composer instead of setting a global default.

### 4. Container configuration

Startup configuration is passed as container command arguments. The main application **does not load `config.yml`**; configure providers and prompts through the console.

The following values describe the current Docker image arguments, not the defaults of a bare binary:

| Argument | Docker deployment value | Purpose |
| --- | --- | --- |
| `--port` | `9024` | HTTP port |
| `--router-prefix` | `/kaguya/api` | API route prefix |
| `--db.kind` | `postgresql` | PostgreSQL connection driver for the pgvector database |
| `--db.host` | `127.0.0.1` | Database host with host networking |
| `--db.port` | `5432` | PostgreSQL port |
| `--db.user` | `pgvector` | Database account |
| `--db.password` | `pgvector-123` | Example password; replace for your deployment |
| `--db.db_name` | `kaguya` | Application database |

To change these values, configure `kaguya-svc` in the Compose file with a `command` override. It replaces the Dockerfile's `CMD`, so include the full application arguments you need, especially `--db.kind=postgresql` and the database connection details. For additional options, run `docker run --rm kaguya:latest --help`.

For a fresh database, keep the database service's `POSTGRES_USER` and `POSTGRES_PASSWORD` aligned with the application's `--db.user` and `--db.password`. The Compose value `POSTGRES_DB=postgres` is the initial database; Kaguya uses its own `kaguya` database. Changing initialization environment variables does not update credentials in an already initialized data directory.

### 5. Logs, updates, and shutdown

```sh
docker compose logs -f --tail=100 kaguya-svc pgvector-svc
docker compose stop
docker compose up -d
```

After updating the source, rebuild and recreate the application container:

```sh
docker build -t kaguya:latest .
docker compose up -d --no-deps kaguya-svc
```

`docker compose down` removes the containers and retains the bind-mounted PostgreSQL data. Keep the `pgvector` data directory across redeployments and back up the database before upgrades.

## Architecture

```text
Browser: React 19 + TypeScript + Ant Design + Tailwind CSS + ECharts
    │  SSE chat / JSON API
    ▼
Go binary: Kong CLI → Gin routes → application services
    ├── Agent runtime (charm.land/fantasy) → configured model provider
    ├── Conversation turns, content blocks, and model context → Ent → PostgreSQL + pgvector
    ├── Provider/model settings, system settings, and usage queries → Ent
    └── Embedded frontend / Swagger / Prometheus
```

| Path | Responsibility |
| --- | --- |
| `main.go`, `internal/cmd/`, `internal/config/` | CLI parsing, startup, and infrastructure configuration |
| `internal/router/`, `internal/api/` | HTTP routes and request/response handling |
| `internal/service/agent/` | Streaming chat, history persistence, context, and title generation |
| `internal/agent/runtime/`, `internal/agent/token/` | Model adapters, execution, and usage recording abstractions |
| `internal/agent/files/` | Workspace-scoped read-only file tools |
| `internal/service/system/` | Provider/model management, system configuration, and analytics |
| `internal/ent/schema/` | Handwritten database schemas; other Ent files are generated |
| `web/` | Web console source and frontend tests |
| `docs/` | Generated Swagger documentation |

The runtime supports `WithTools` and includes `list_files`, `read_file`, `grep_files`, and `file_metadata` tools with workspace path restrictions. **The current chat service does not register these tools by default.**

## API

With the default route prefix, useful endpoints are:

| Endpoint | Purpose |
| --- | --- |
| `POST /kaguya/api/v1/chat/sse` | SSE streaming chat |
| `GET /kaguya/api/v1/chat/ws` | WebSocket chat |
| `GET /kaguya/api/v1/chat/conversation/page` | Paginated conversation list |
| `GET /kaguya/api/v1/chat/conversation/:id/turns` | Conversation turns |
| `GET /kaguya/api/v1/chat/conversation/:id/context` | Conversation context statistics |
| `GET /kaguya/api/v1/project/page` | Project list (name prefix and pagination) |
| `GET /kaguya/api/v1/project/directories` | Browse folders within the server user's home |
| `POST /kaguya/api/v1/project` | Create project |
| `GET / PUT / DELETE /kaguya/api/v1/project/:id` | Project details, update, and delete |
| `GET /kaguya/api/v1/system/provider/page` | Provider list |
| `GET /kaguya/api/v1/system/model/page` | Model list |
| `GET /kaguya/api/v1/system/usage` | Token analytics |
| `GET /kaguya/api/v1/system/info` | System settings (`PUT` updates them) |
| `/kaguya/api/swagger/index.html` | Swagger UI |
| `/kaguya/api/metrics` | Prometheus metrics |

Conversation listing accepts `project_id`; new SSE/WS chats accept `project_id` to select their project. Continuing a saved conversation retains its stored association.

After configuring a default model, start a conversation with:

```sh
curl -N http://localhost:9024/kaguya/api/v1/chat/sse \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"flag":"chat","messages":"Hello, Kaguya"}'
```

Reuse the returned conversation `id` in subsequent request bodies to continue its history. To select a model explicitly, pass `model_id` with the local model record ID. Stream frames use `start`, `delta`, `done`, and `error`; `done` is emitted only after the completed turn has been saved. See the [route definitions](internal/router/v1/) and [chat DTOs](internal/dto/chat/) for the current API contract.

## Development

Source development requires Go 1.26.4 or later, Bun, and Make. Use the Docker Compose deployment above for the running backend and database.

Run `make install` from the repository root to build both frontend and backend and install the binary to `/usr/bin/<repository-directory-name>` (usually `/usr/bin/kaguya`) with mode `0755`. This requires write access to `/usr/bin`, with Go and Bun available in the execution environment; it does not start the service.

```sh
make test                  # Go tests with the race detector
cd web
bun install --frozen-lockfile
bun run test               # Frontend tests
bun run lint               # oxlint
bun run build              # TypeScript checks and Vite production build
bun run dev                # Vite development server
```

For frontend development, keep the application container running on port `9024`; Vite proxies `/kaguya/api` to `http://localhost:9024`. If the API prefix or deployment location changes, keep the frontend `VITE_API_BASE_URL` and proxy configuration aligned. Rebuild the Docker image after backend changes to update the running service.

After changing Ent schemas, run `go generate ./internal/ent` from the repository root. Keep generated Ent code in sync with the schemas.

## Current scope

- The console is currently Chinese; English documentation does not imply an English UI.
- Chat input is text. Model capability labels do not constitute image uploads, a knowledge base, or automatically enabled file tools.
- Access-log code and an API exist, but the access-log middleware is currently disabled and the page is not exposed in the navigation.
- The current routes do not provide built-in user login or per-user access isolation. This is an experimental console, not a complete multi-tenant service.

## Why Kaguya?

**Kaguya** comes from **Kaguya-hime / 辉夜姬**, the quiet, elegant, and mysterious moon princess. In *Dragon Raja* (《龙族》), the name is also associated with the Japanese branch's super artificial intelligence system. The project borrows that image for a calm, rational, and controllable Agent core.

## License

[MIT](LICENSE).
