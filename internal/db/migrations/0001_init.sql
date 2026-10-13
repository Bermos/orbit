-- Orbit initial schema.
--
-- Two trees are kept apart on purpose: the resource tree (applications and
-- everything under them) has stable keys that never change when the org tree
-- (teams) is reorganised. The ownership mapping joins the two.

create table users (
    id            uuid primary key default gen_random_uuid(),
    username      text not null unique check (username ~ '^[a-z0-9][a-z0-9._-]{1,38}$'),
    display_name  text not null,
    password_hash text not null,
    is_admin      boolean not null default false,
    created_at    timestamptz not null default now()
);

create table sessions (
    token_hash text primary key,
    user_id    uuid not null references users (id) on delete cascade,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null
);

-- Org tree. Slugs and names can change freely; nothing in the resource tree
-- refers to them.
create table teams (
    id         uuid primary key default gen_random_uuid(),
    slug       text not null unique check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name       text not null,
    parent_id  uuid references teams (id) on delete restrict,
    created_at timestamptz not null default now(),
    check (parent_id is null or parent_id <> id)
);

create table team_members (
    team_id uuid not null references teams (id) on delete cascade,
    user_id uuid not null references users (id) on delete cascade,
    role    text not null default 'member' check (role in ('member', 'lead')),
    primary key (team_id, user_id)
);

-- Resource tree root. The key (short name) is the URL slug and the ID and
-- never changes; the name (long name) is what the UI shows.
create table applications (
    key               text primary key check (key ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$'),
    name              text not null,
    description       text not null default '',
    owner_team_id     uuid not null references teams (id) on delete restrict,
    next_issue_number integer not null default 1,
    created_at        timestamptz not null default now()
);

-- Ownership mapping: sub-teams responsible for an application.
create table application_teams (
    app_key text not null references applications (key) on delete cascade,
    team_id uuid not null references teams (id) on delete cascade,
    role    text not null check (role in ('developer', 'maintainer')),
    primary key (app_key, team_id)
);

-- Direct exceptions, always visible on the application.
create table application_grants (
    app_key text not null references applications (key) on delete cascade,
    user_id uuid not null references users (id) on delete cascade,
    role    text not null check (role in ('reader', 'developer', 'maintainer', 'owner')),
    primary key (app_key, user_id)
);

create table repositories (
    id             uuid primary key default gen_random_uuid(),
    app_key        text not null references applications (key) on delete cascade,
    name           text not null check (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    description    text not null default '',
    default_branch text not null default 'main',
    created_at     timestamptz not null default now(),
    unique (app_key, name)
);

-- Every discussion is a channel: chat rooms, issue discussions and code
-- discussions share one primitive so a conversation can move between them.
create table channels (
    id         uuid primary key default gen_random_uuid(),
    app_key    text references applications (key) on delete cascade,
    kind       text not null check (kind in ('chat', 'issue')),
    name       text not null,
    created_at timestamptz not null default now()
);
create unique index channels_chat_name on channels (coalesce(app_key, ''), name) where kind = 'chat';

create table messages (
    id          uuid primary key default gen_random_uuid(),
    channel_id  uuid not null references channels (id) on delete cascade,
    thread_id   uuid references messages (id) on delete cascade,
    author_id   uuid references users (id) on delete set null,
    body        text not null,
    attachments jsonb not null default '[]',
    created_at  timestamptz not null default now()
);
create index messages_channel on messages (channel_id, created_at);
create index messages_thread on messages (thread_id, created_at) where thread_id is not null;

create table issues (
    id          uuid primary key default gen_random_uuid(),
    app_key     text not null references applications (key) on delete cascade,
    number      integer not null,
    title       text not null,
    body        text not null default '',
    status      text not null default 'open' check (status in ('open', 'in_progress', 'done', 'closed')),
    author_id   uuid references users (id) on delete set null,
    assignee_id uuid references users (id) on delete set null,
    channel_id  uuid not null references channels (id),
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    unique (app_key, number)
);

create table todos (
    id         uuid primary key default gen_random_uuid(),
    user_id    uuid not null references users (id) on delete cascade,
    title      text not null,
    done       boolean not null default false,
    created_at timestamptz not null default now(),
    done_at    timestamptz
);
create index todos_user on todos (user_id, done, created_at);

-- The link graph. Any entity can point at any other; context carries the
-- anchor (for example the exact message or code range) so the UI can jump
-- straight back to it.
create table links (
    id         uuid primary key default gen_random_uuid(),
    from_type  text not null,
    from_id    text not null,
    to_type    text not null,
    to_id      text not null,
    kind       text not null,
    context    jsonb not null default '{}',
    created_by uuid references users (id) on delete set null,
    created_at timestamptz not null default now(),
    unique (from_type, from_id, to_type, to_id, kind)
);
create index links_from on links (from_type, from_id);
create index links_to on links (to_type, to_id);
