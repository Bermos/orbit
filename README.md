# Orbit

Orbit is a self-hosted platform where code, chat, issues, planning, reviews and CI are one product sharing one data model, not separate tools bolted together.

- **Everything links to everything.** Turn a chat message into a to-do or an issue, or select lines in the code view and discuss them in a channel or on an issue. Every link works both ways, and opening a to-do takes you straight back to the highlighted message it came from.
- **API first.** The web UI uses the same public REST and WebSocket API that integrations use.
- **Stable paths, flexible ownership.** Applications are the stable root of the resource tree (`/<app>/<repo>`). Teams and sub-teams form a separate tree you can rename, merge or move freely. An ownership mapping joins the two, so a reorg changes who has access but never changes a URL.
- **Cloud native.** One stateless Go binary with the web UI embedded, backed by PostgreSQL and storage for git repositories.

> Status: early prototype. Expect breaking changes.

## What works today

- Local accounts (the first account becomes the instance admin)
- Teams and sub-teams, members and leads, and renaming or moving teams
- Applications with a short name (URL slug and ID, immutable) and a long display name, an owning team and responsible sub-teams
- Team spaces: one view of every application, and its open issues, that a team or its sub-teams own or are responsible for
- Git hosting over HTTPS (`git clone` and `git push` with your Orbit username and password) and a code browser
- Channels with threads and live updates
- Issues with status, assignee and a discussion
- To-dos created from messages, issues or code selections
- Discussing a code range: the range is pinned to its commit and the lines are kept with the message
- A link graph with backlinks on every message and issue
- Full-text search over applications, issues and messages
- Agents in chat: @mention an agent in a channel or issue discussion and it answers in the thread, using Orbit's own tools (see [Agents](#agents))

Not yet: code review, CI, agents that change code, epics and boards, notifications, OIDC and LDAP, and a Helm chart.

## Run it

With Docker:

```sh
docker compose up --build
```

Then open http://localhost:8080 and create an account.

For development you need Go 1.26+, Node 22+, git, and a PostgreSQL 16 database:

```sh
# backend (API on :8080)
export ORBIT_DATABASE_URL=postgres://orbit:orbit@localhost:5432/orbit?sslmode=disable
go run ./cmd/orbit

# frontend with hot reload (http://localhost:5173, proxies /api and /git to :8080)
cd web && npm install && npm run dev
```

To serve the UI from the Go binary, run `npm run build` in `web/` before `go build ./cmd/orbit`; the build output is embedded.

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `ORBIT_ADDR` | `:8080` | Listen address |
| `ORBIT_DATABASE_URL` | `postgres://orbit:orbit@localhost:5432/orbit?sslmode=disable` | PostgreSQL connection string |
| `ORBIT_DATA_DIR` | `./data` | Where git repositories are stored (`<dir>/repos/<app>/<repo>.git`) |
| `ORBIT_BASE_URL` | `http://localhost:8080` | External URL, used for clone URLs and secure cookies |
| `ORBIT_AGENTS_DIR` | | Directory of agent definition files (`*.md`) added to the built-in agents |
| `ANTHROPIC_API_KEY` | | Key for agents whose model is `anthropic/...`. Without it they answer with a built-in test model |

Migrations run automatically on startup.

## Agents

An agent is a member of the instance that people @mention in channels and issue discussions. It answers in the thread of the message that mentioned it, and every tool call it made is listed under the answer.

Agents are defined as files: Markdown with YAML front matter, the body being the agent's instructions. Orbit ships one, [`assistant`](internal/agent/catalog/assistant.md). Put more in `ORBIT_AGENTS_DIR`; a file with the same name replaces a built-in one.

```markdown
---
name: ledger-helper          # the @mention name
display_name: Ledger helper
description: Knows the ledger application.
model: anthropic/claude-opus-5-5   # <provider>/<model>, or "fake"
effort: low                  # optional, passed to the model
role: reader                 # the highest role the agent may act with
apps: [ledger]               # applications it may use; "*" for all
tools: [search, get_issue, read_file, propose_issue]   # omit for all tools
max_steps: 8                 # model calls per answer
---
You help the ledger team. Cite issues as ledger#123 and files with line numbers.
```

**Permissions.** An agent acts for the person who asked, with the minimum of that person's role and its own `role` and `apps`. Neither can use the other to reach more: a narrow agent stays narrow when an admin asks, and a broad agent cannot read more than the asker.

**Shared channels and "answer only me".** In a shared conversation an agent only uses what everyone in it can read, so personal data (to-dos, private messages) never reaches the answer. Ticking *Answer only me* in the composer makes the question and the answer visible to the asker alone, and the agent may then use everything the asker can read.

**Writes are proposals.** Agents cannot change anything themselves. They draft issues, to-dos and issue comments, shown under the answer with a button; only the person who asked can confirm, and confirming runs the write through the public API with their permissions. The created entity links back to the conversation and to the agent run.

**Tools** are a small typed set over the public API, run in-process as the asker: `search`, `list_issues`, `get_issue`, `get_thread`, `get_application`, `list_files`, `read_file`, `list_my_todos` (private questions only), `propose_issue`, `propose_todo` and `propose_issue_comment`.

**Models.** Providers are pluggable (`internal/agent`). Orbit includes an Anthropic provider and a deterministic `fake` provider that tests and local runs use, so the whole flow works without credentials.

Changing code is not possible from chat; that comes with CI, as agent jobs.

### Tests

```sh
ORBIT_TEST_DATABASE_URL=postgres://orbit:orbit@localhost:5432/orbit_test?sslmode=disable go test ./...
```

The tests drop and recreate the schema of that database, so point it at a disposable one.

## Layout

```
cmd/orbit           server entrypoint
internal/db         connection pool and embedded SQL migrations
internal/access     role resolution through the ownership mapping
internal/agent      agent definitions, model providers and the tool-use loop
internal/gitrepo    bare repositories on disk, smart HTTP, browsing
internal/realtime   WebSocket event hub
internal/server     REST API handlers, the link graph, and agents' tools and runs
web/                Vue 3 + TypeScript UI (embedded into the binary)
```

## License

[AGPL-3.0](LICENSE)
