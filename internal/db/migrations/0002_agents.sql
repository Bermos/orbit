-- Agents as members, private answers, agent runs and proposed writes.

-- Agents are users of kind 'agent': they author messages and can be
-- @mentioned, but cannot sign in. Their definitions live in files.
alter table users add column kind text not null default 'human' check (kind in ('human', 'agent'));

-- A message with visible_to set is shown only to that user. This is how
-- "answer only me" works: the question and the agent's answer stay private.
alter table messages add column visible_to uuid references users (id) on delete cascade;

-- One agent run answers one message. It records who asked, on whose behalf
-- the agent acted, every tool call and what it cost.
create table agent_runs (
    id                 uuid primary key default gen_random_uuid(),
    agent_id           uuid not null references users (id) on delete cascade,
    asker_id           uuid references users (id) on delete set null,
    channel_id         uuid not null references channels (id) on delete cascade,
    trigger_message_id uuid references messages (id) on delete set null,
    reply_message_id   uuid references messages (id) on delete set null,
    private            boolean not null,
    status             text not null default 'running' check (status in ('running', 'done', 'failed')),
    model              text not null default '',
    steps              jsonb not null default '[]',
    input_tokens       bigint not null default 0,
    output_tokens      bigint not null default 0,
    error              text not null default '',
    created_at         timestamptz not null default now(),
    finished_at        timestamptz
);
create index agent_runs_reply on agent_runs (reply_message_id);

-- Writes an agent drafted. Nothing happens until the asker confirms; the
-- write then runs through the public API with the asker's permissions.
create table agent_proposals (
    id         uuid primary key default gen_random_uuid(),
    run_id     uuid not null references agent_runs (id) on delete cascade,
    kind       text not null check (kind in ('issue', 'todo', 'issue_comment')),
    input      jsonb not null,
    status     text not null default 'pending' check (status in ('pending', 'applying', 'applied', 'dismissed')),
    result     jsonb,
    created_at timestamptz not null default now(),
    decided_at timestamptz
);
create index agent_proposals_run on agent_proposals (run_id, created_at);

-- Full-text search, used by the search API and the agents' search tool.
create index issues_fts on issues using gin (to_tsvector('english', title || ' ' || body));
create index messages_fts on messages using gin (to_tsvector('english', body));
create index applications_fts on applications using gin (to_tsvector('english', key || ' ' || name || ' ' || description));
