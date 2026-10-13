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

Not yet: code review, CI, epics and boards, notifications, search, OIDC and LDAP, and a Helm chart.

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

Migrations run automatically on startup.

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
internal/gitrepo    bare repositories on disk, smart HTTP, browsing
internal/realtime   WebSocket event hub
internal/server     REST API handlers and the link graph
web/                Vue 3 + TypeScript UI (embedded into the binary)
```

## License

[AGPL-3.0](LICENSE)
