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
- CI: pipelines from `.orbit/pipeline.yml` run on Kubernetes by pull-based runners, a checks API and webhooks for any other CI, personal access tokens, and agents that change code as CI jobs (see [CI](#ci))

Not yet: code review and merge requests, epics and boards, notifications, OIDC and LDAP, and a Helm chart.

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
| `ORBIT_EMBEDDED_RUNNER` | | `local` runs a CI runner inside the server, executing jobs as unisolated processes. For development only |

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

Agents with `runs: job` change code: see [Agent jobs](#agent-jobs).

## CI

Orbit owns the CI control plane (pipelines, scheduling, logs, the UI) and hands execution to runners. Every CI result, Orbit's own or an external system's, is a *check* on a commit, so commits show one status whatever produced it.

### Pipelines

A repository defines its pipeline in `.orbit/pipeline.yml`. Every push to a branch it applies to starts one; it can also be started by hand from the repository's Pipelines tab.

```yaml
when:
  branches: [main, "release/*"]   # optional; default: every branch
jobs:
  test:
    image: golang:1.26
    script:
      - go vet ./...
      - go test ./...
    env:
      GOFLAGS: -mod=mod
  build:
    image: golang:1.26
    needs: [test]                 # waits for test to succeed
    script: [go build ./...]
    timeout: 20m                  # default 30m
  review:
    agent: reviewer               # an agent job, see below
    prompt: Check this change for missing tests.
```

Jobs without `needs` run in parallel; a job whose needs did not succeed is skipped. Each job reports as the check `orbit/<job>` on the commit. Jobs see `CI`, `ORBIT_APP`, `ORBIT_REPO`, `ORBIT_COMMIT`, `ORBIT_REF`, `ORBIT_PIPELINE_NUMBER` and `ORBIT_JOB`. A broken pipeline file shows up as a failed pipeline with the reason.

### Runners

Runners pull jobs from Orbit over HTTPS, so they need no inbound access and can run in any cluster that reaches Orbit. An instance admin registers shared runners (Settings), a maintainer registers runners for their application (the application's CI tab); either way the runner's token is shown once.

`orbit-runner` runs each job as a pod: an init container clones the commit with a short-lived job token (valid for that one repository while the job runs), then the job's script runs in the job's image. Pods get no service account token, no privilege escalation and no capabilities. [`deploy/kubernetes/runner.yaml`](deploy/kubernetes/runner.yaml) has the namespace, RBAC and Deployment.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ORBIT_URL` | | Orbit's URL as the runner and its pods reach it |
| `ORBIT_RUNNER_TOKEN` | | The runner's token |
| `ORBIT_RUNNER_EXECUTOR` | `kubernetes` | `kubernetes`, or `local` to run jobs as processes on the runner's machine (development) |
| `ORBIT_RUNNER_CONCURRENCY` | `2` | Jobs run at once |
| `ORBIT_RUNNER_NAMESPACE` | the runner's own | Namespace for job pods |
| `ORBIT_RUNNER_RUNTIME_CLASS` | | `runtimeClassName` for job pods, for example `gvisor` |
| `ORBIT_RUNNER_CPU_LIMIT`, `ORBIT_RUNNER_MEMORY_LIMIT` | | Limits for the job container |
| `ORBIT_RUNNER_AGENT_IMAGE` | `golang:1.26` | Image for agent jobs whose agent names none (it needs `sh`) |
| `ORBIT_RUNNER_GIT_IMAGE` | `alpine/git:2.54.0` | Image that clones repositories |

For a quick local try, start the server with `ORBIT_EMBEDDED_RUNNER=local`.

### External CI: checks API, webhooks and tokens

Keep an existing CI while you migrate. Create a personal access token (Settings), then report results on a commit:

```sh
curl -X POST "$ORBIT/api/v1/apps/ledger/repos/api/commits/$SHA/checks" \
  -H "Authorization: Bearer $ORBIT_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "jenkins/build", "status": "success", "summary": "412 tests passed", "details_url": "https://ci.example.com/42"}'
```

`status` is one of `pending`, `running`, `success`, `failure`, `error`, `cancelled`, `skipped`; reporting the same `name` again updates the check. Reporting needs the developer role; `orbit/` names are reserved. Personal access tokens also work as the password for `git` over HTTPS.

Webhooks (an application's CI tab, maintainers) send `push`, `check` and `pipeline` events, optionally for one repository only, to trigger and follow external systems. Each request carries `X-Orbit-Event` and `X-Orbit-Signature: sha256=<HMAC-SHA256 of the body with the hook's secret>`. Deliveries are attempted once and listed under the hook.

### Agent jobs

An agent job is an agent working on a fresh checkout of a repository, on the same engine as other jobs. Agents whose definition says `runs: job` start one when they are @mentioned; Orbit ships one, [`coder`](internal/agent/catalog/coder.md). Pipeline files can run them too (`agent:` and `prompt:` instead of `script:`), acting for whoever triggered the pipeline.

```
@coder fix the rounding in ledger/api@release/2.3, see ledger#41
```

The repository is the one named as `app/repo` (optionally `@branch`), otherwise the code the conversation discusses, otherwise the conversation's application's only repository. Starting a job needs the developer role.

- **The model stays on the server.** Orbit runs the agent loop with the same rule as chat: the agent acts for the person with the minimum of their role and its own. Tools that touch the checkout (`list_files`, `read_file`, `write_file`, `edit_file`, `run`) are sent to the runner and run in the job's pod; Orbit's read tools (`search`, `get_issue`, ...) run on the server. Model credentials never reach the runner.
- **Progress shows where it was asked.** The reply in the thread follows the job, and every tool call lands in the job's log.
- **Nothing is pushed without a person.** The runner hands back the changes as a diff. The reply offers to push them as a branch (`agent/<agent>-<pipeline>`); only the person who asked can confirm, which needs the developer role for both them and the agent. The commit is authored by the agent and committed by that person, and the new branch runs its pipeline like any push.
- **Bounded.** Jobs time out (30 minutes by default), and `max_steps` and `budget_tokens` in the agent's definition cap model calls and tokens.

Agent pods carry the label `orbit.dev/kind=agent`, so a NetworkPolicy can restrict their egress; the runner manifest includes an example.

Not yet: pausing a job to ask a person a question, merge requests for agent branches, artifacts and caches, secrets for jobs, retrying failed webhook deliveries, and an importer for `.gitlab-ci.yml`.

### Tests

```sh
ORBIT_TEST_DATABASE_URL=postgres://orbit:orbit@localhost:5432/orbit_test?sslmode=disable go test ./...
```

The tests drop and recreate the schema of that database, so point it at a disposable one.

## Layout

```
cmd/orbit           server entrypoint
cmd/orbit-runner    CI runner
internal/db         connection pool and embedded SQL migrations
internal/access     role resolution through the ownership mapping
internal/agent      agent definitions, model providers and the tool-use loop
internal/gitrepo    bare repositories on disk, smart HTTP, browsing
internal/pipeline   pipeline files and the runner protocol
internal/runner     the runner and its executors (Kubernetes, local)
internal/realtime   WebSocket event hub
internal/server     REST API handlers, the link graph, agents' tools and runs, and the CI control plane
deploy/kubernetes   manifests for the runner
web/                Vue 3 + TypeScript UI (embedded into the binary)
```

## License

[AGPL-3.0](LICENSE)
