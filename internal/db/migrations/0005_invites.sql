-- Invites: single-use links an admin hands out to create an account, so an
-- instance on the internet does not let anyone sign up.

create table invites (
    id         uuid primary key default gen_random_uuid(),
    token_hash text not null unique,
    note       text not null default '',
    created_by uuid not null references users (id) on delete cascade,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null,
    used_at    timestamptz,
    used_by    uuid references users (id) on delete set null
);
