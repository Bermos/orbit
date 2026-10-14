-- Private chats with agents. A direct channel is one person's conversation
-- with one agent; a person can keep several per agent. Every message in it
-- is visible to that person only, so search, links and agent tools treat it
-- like any other private message.

alter table channels drop constraint channels_kind_check;
alter table channels add constraint channels_kind_check check (kind in ('chat', 'issue', 'direct'));
alter table channels add column owner_id uuid references users (id) on delete cascade;
alter table channels add column agent_id uuid references users (id) on delete cascade;
alter table channels add column last_message_at timestamptz;
alter table channels add constraint channels_direct check ((kind = 'direct') = (owner_id is not null and agent_id is not null));
create index channels_direct_owner on channels (owner_id, last_message_at desc) where kind = 'direct';
