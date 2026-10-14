-- CI: access tokens, the checks API, webhooks, native pipelines, runners and
-- agent jobs.

-- Personal access tokens, for scripts and external CI systems. They act as
-- the user who created them; only the hash is stored.
create table api_tokens (
    id           uuid primary key default gen_random_uuid(),
    user_id      uuid not null references users (id) on delete cascade,
    name         text not null check (length(name) between 1 and 80),
    token_hash   text not null unique,
    created_at   timestamptz not null default now(),
    expires_at   timestamptz,
    last_used_at timestamptz
);
create index api_tokens_user on api_tokens (user_id, created_at);

-- A check is one named result for one commit: "build", "ci/lint",
-- "orbit/test". External CI writes them through the API; Orbit's own
-- pipelines write one per job. Reporting the same name again updates it.
create table checks (
    id          uuid primary key default gen_random_uuid(),
    repo_id     uuid not null references repositories (id) on delete cascade,
    commit_sha  text not null check (commit_sha ~ '^[0-9a-f]{40}$'),
    name        text not null check (length(name) between 1 and 100),
    status      text not null check (status in ('pending', 'running', 'success', 'failure', 'error', 'cancelled', 'skipped')),
    summary     text not null default '',
    details_url text not null default '',
    job_id      uuid,
    created_by  uuid references users (id) on delete set null,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    unique (repo_id, commit_sha, name)
);
create index checks_commit on checks (repo_id, commit_sha);

-- Outgoing webhooks of an application, optionally limited to one repository.
-- Payloads are signed with the secret (HMAC-SHA256).
create table webhooks (
    id         uuid primary key default gen_random_uuid(),
    app_key    text not null references applications (key) on delete cascade,
    repo_name  text,
    url        text not null,
    secret     text not null,
    events     text[] not null,
    active     boolean not null default true,
    created_by uuid references users (id) on delete set null,
    created_at timestamptz not null default now()
);
create index webhooks_app on webhooks (app_key);

create table webhook_deliveries (
    id          uuid primary key default gen_random_uuid(),
    webhook_id  uuid not null references webhooks (id) on delete cascade,
    event       text not null,
    status_code integer not null default 0,
    error       text not null default '',
    duration_ms integer not null default 0,
    created_at  timestamptz not null default now()
);
create index webhook_deliveries_hook on webhook_deliveries (webhook_id, created_at desc);

alter table repositories add column next_pipeline_number integer not null default 1;

-- A pipeline is one run of a repository's .orbit/pipeline.yml (or a single
-- agent job started from chat) against one commit.
create table pipelines (
    id           uuid primary key default gen_random_uuid(),
    repo_id      uuid not null references repositories (id) on delete cascade,
    number       integer not null,
    commit_sha   text not null,
    ref          text not null,
    trigger      text not null check (trigger in ('push', 'manual', 'agent')),
    triggered_by uuid references users (id) on delete set null,
    status       text not null default 'pending' check (status in ('pending', 'running', 'success', 'failure', 'error', 'cancelled')),
    error        text not null default '',
    created_at   timestamptz not null default now(),
    finished_at  timestamptz,
    unique (repo_id, number)
);
create index pipelines_repo on pipelines (repo_id, number desc);

-- Runners pull jobs. A runner without an application is shared by the whole
-- instance; one with an application only runs that application's jobs.
create table runners (
    id           uuid primary key default gen_random_uuid(),
    name         text not null check (length(name) between 1 and 80),
    app_key      text references applications (key) on delete cascade,
    token_hash   text not null unique,
    executor     text not null default '',
    version      text not null default '',
    last_seen_at timestamptz,
    created_by   uuid references users (id) on delete set null,
    created_at   timestamptz not null default now()
);

create table jobs (
    id          uuid primary key default gen_random_uuid(),
    pipeline_id uuid not null references pipelines (id) on delete cascade,
    name        text not null,
    kind        text not null check (kind in ('script', 'agent')),
    spec        jsonb not null,
    needs       text[] not null default '{}',
    status      text not null default 'pending' check (status in ('pending', 'running', 'success', 'failure', 'error', 'cancelled', 'skipped')),
    runner_id   uuid references runners (id) on delete set null,
    -- The job token lets the runner clone this one repository and report
    -- on this one job, and only while the job runs.
    token_hash  text unique,
    exit_code   integer,
    error       text not null default '',
    -- What an agent job changed, as a binary git diff against commit_sha.
    patch       bytea,
    created_at  timestamptz not null default now(),
    started_at  timestamptz,
    -- Runners report at least every 30 seconds while they work on a job.
    heartbeat_at timestamptz,
    cancel_requested boolean not null default false,
    finished_at timestamptz,
    unique (pipeline_id, name)
);
create index jobs_queue on jobs (created_at) where status = 'pending';

alter table checks add constraint checks_job foreign key (job_id) references jobs (id) on delete set null;

-- Logs arrive in chunks while the job runs.
create table job_logs (
    job_id uuid not null references jobs (id) on delete cascade,
    seq    integer not null,
    data   text not null,
    primary key (job_id, seq)
);

-- Agent runs can now belong to a job instead of (or as well as) a chat
-- conversation, and agents can propose pushing a branch.
alter table agent_runs alter column channel_id drop not null;
alter table agent_runs add column job_id uuid references jobs (id) on delete set null;
alter table agent_proposals drop constraint agent_proposals_kind_check;
alter table agent_proposals add constraint agent_proposals_kind_check check (kind in ('issue', 'todo', 'issue_comment', 'branch'));
