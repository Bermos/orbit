-- Merge requests: a proposal to merge one branch into another, reviewed in
-- place. Like an issue, a merge request has a discussion channel; inline
-- comments on the diff are messages in it that carry the code range they
-- discuss, so threads, agents, to-dos and issues work on them as anywhere.

alter table channels drop constraint channels_kind_check;
alter table channels add constraint channels_kind_check check (kind in ('chat', 'issue', 'direct', 'merge_request'));

-- Numbered per application next to issues: ledger#12 is an issue,
-- ledger!3 a merge request.
alter table applications add column next_mr_number integer not null default 1;

create table merge_requests (
    id               uuid primary key default gen_random_uuid(),
    app_key          text not null references applications (key) on delete cascade,
    repo_id          uuid not null references repositories (id) on delete cascade,
    number           integer not null,
    title            text not null,
    body             text not null default '',
    source_branch    text not null,
    target_branch    text not null,
    status           text not null default 'open' check (status in ('open', 'merged', 'closed')),
    draft            boolean not null default false,
    author_id        uuid references users (id) on delete set null,
    channel_id       uuid not null references channels (id),
    -- The source branch's tip and its merge base with the target, as of the
    -- last push. Inline comments anchor to one of these two commits.
    head_sha         text not null,
    base_sha         text not null,
    merge_commit_sha text,
    merged_by        uuid references users (id) on delete set null,
    merged_at        timestamptz,
    closed_at        timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now(),
    unique (app_key, number),
    check (source_branch <> target_branch)
);
create index merge_requests_repo on merge_requests (repo_id, status);
create unique index merge_requests_open_pair on merge_requests (repo_id, source_branch, target_branch) where status = 'open';

-- One verdict per reviewer, replaced when they review again. head_sha says
-- which version they reviewed.
create table merge_request_reviews (
    merge_request_id uuid not null references merge_requests (id) on delete cascade,
    user_id          uuid not null references users (id) on delete cascade,
    state            text not null check (state in ('approved', 'changes_requested')),
    head_sha         text not null,
    created_at       timestamptz not null default now(),
    primary key (merge_request_id, user_id)
);

-- Agents can propose pushing their changes to a merge request's branch.
alter table agent_proposals drop constraint agent_proposals_kind_check;
alter table agent_proposals add constraint agent_proposals_kind_check check (kind in ('issue', 'todo', 'issue_comment', 'branch', 'merge_request_push'));
