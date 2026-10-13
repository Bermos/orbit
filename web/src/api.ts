// Typed client for Orbit's REST API. The UI uses exactly the same API that
// integrations use.

export interface User {
  id: string
  username: string
  display_name: string
  is_admin: boolean
}
export type Author = Pick<User, 'id' | 'username' | 'display_name'>

export interface Team {
  id: string
  slug: string
  name: string
  parent_id: string | null
  member_count: number
}
export interface TeamMember extends User {
  role: 'member' | 'lead'
}
export interface TeamDetail {
  team: Team
  members: TeamMember[]
  children: Team[]
  ancestors: Team[]
}
export interface TeamRef {
  id: string
  name: string
}
export interface App {
  key: string
  name: string
  description: string
  owner_team: TeamRef
  created_at: string
}
export interface SpaceApp extends App {
  relation: string
  via_id: string
  via_name: string
}
export interface Repo {
  id: string
  app_key: string
  name: string
  description: string
  default_branch: string
  created_at: string
}
export interface Channel {
  id: string
  app_key: string | null
  kind: 'chat' | 'issue'
  name: string
  created_at: string
}
export interface AppDetail {
  application: App
  responsible_teams: (TeamRef & { role: string })[]
  repositories: Repo[]
  channels: Channel[]
  my_role: Role
}
export type Role = 'none' | 'reader' | 'developer' | 'maintainer' | 'owner'
export const roleRank: Record<Role, number> = { none: 0, reader: 1, developer: 2, maintainer: 3, owner: 4 }

export interface Ref {
  type: string
  id: string
}
export interface Entity extends Ref {
  title: string
  subtitle: string
  url: string
  missing?: boolean
}
export interface CodeRange {
  app: string
  repo: string
  commit: string
  path: string
  start: number
  end: number
}
export type Attachment =
  | { type: 'code'; ref: Ref; code: CodeRange; lines: string[]; url: string }
  | { type: 'entity'; entity: Entity; quote?: string }

export interface Message {
  id: string
  channel_id: string
  thread_id: string | null
  author: Author | null
  body: string
  attachments: Attachment[]
  reply_count: number
  link_count: number
  created_at: string
}
export type IssueStatus = 'open' | 'in_progress' | 'done' | 'closed'
export interface Issue {
  id: string
  app_key: string
  number: number
  title: string
  body: string
  status: IssueStatus
  author: Author | null
  assignee: Author | null
  channel_id: string
  created_at: string
  updated_at: string
}
export interface LinkView {
  id: string
  kind: string
  direction: 'in' | 'out'
  entity: Entity
  context: Record<string, unknown>
  created_at: string
}
export interface Todo {
  id: string
  title: string
  done: boolean
  created_at: string
  done_at: string | null
  source: Entity | null
}
export interface Branch {
  name: string
  commit: string
}
export interface RepoDetail {
  repository: Repo
  clone_url: string
  branches: Branch[]
  empty: boolean
}
export interface TreeEntry {
  name: string
  path: string
  type: 'blob' | 'tree'
  size: number
}
export interface Commit {
  sha: string
  author: string
  email: string
  date: string
  subject: string
}
export interface CodeInput {
  app: string
  repo: string
  ref: string
  path: string
  start: number
  end: number
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch('/api/v1' + path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/auth/')) {
      window.dispatchEvent(new CustomEvent('orbit:unauthorized'))
    }
    throw new ApiError(res.status, data.error ?? res.statusText)
  }
  return data as T
}

const q = (params: Record<string, string | undefined>) => {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v) s.set(k, v)
  const str = s.toString()
  return str ? '?' + str : ''
}

export const api = {
  register: (username: string, display_name: string, password: string) =>
    request<{ user: User }>('POST', '/auth/register', { username, display_name, password }),
  login: (username: string, password: string) => request<{ user: User }>('POST', '/auth/login', { username, password }),
  logout: () => request<void>('POST', '/auth/logout'),
  me: () => request<User>('GET', '/me'),
  myTeams: () => request<Team[]>('GET', '/me/teams'),
  users: () => request<User[]>('GET', '/users'),

  teams: () => request<Team[]>('GET', '/teams'),
  team: (id: string) => request<TeamDetail>('GET', `/teams/${id}`),
  createTeam: (slug: string, name: string, parent_id: string | null) =>
    request<TeamDetail>('POST', '/teams', { slug, name, parent_id }),
  updateTeam: (id: string, patch: { slug?: string; name?: string; parent_id?: string }) =>
    request<TeamDetail>('PATCH', `/teams/${id}`, patch),
  teamSpace: (id: string) => request<{ applications: SpaceApp[]; open_issues: Issue[] }>('GET', `/teams/${id}/space`),
  putTeamMember: (team: string, user: string, role: string) =>
    request<TeamDetail>('PUT', `/teams/${team}/members/${user}`, { role }),
  removeTeamMember: (team: string, user: string) => request<TeamDetail>('DELETE', `/teams/${team}/members/${user}`),

  apps: () => request<App[]>('GET', '/apps'),
  app: (key: string) => request<AppDetail>('GET', `/apps/${key}`),
  createApp: (input: { key: string; name: string; description: string; owner_team_id: string }) =>
    request<AppDetail>('POST', '/apps', input),
  updateApp: (key: string, patch: { name?: string; description?: string; owner_team_id?: string }) =>
    request<AppDetail>('PATCH', `/apps/${key}`, patch),
  putAppTeam: (key: string, team: string, role: string) => request<AppDetail>('PUT', `/apps/${key}/teams/${team}`, { role }),
  removeAppTeam: (key: string, team: string) => request<AppDetail>('DELETE', `/apps/${key}/teams/${team}`),

  createRepo: (app: string, name: string, description: string) =>
    request<RepoDetail>('POST', `/apps/${app}/repos`, { name, description }),
  repo: (app: string, repo: string) => request<RepoDetail>('GET', `/apps/${app}/repos/${repo}`),
  tree: (app: string, repo: string, ref: string, path: string) =>
    request<{ commit: string; path: string; entries: TreeEntry[] }>('GET', `/apps/${app}/repos/${repo}/tree${q({ ref, path })}`),
  blob: (app: string, repo: string, ref: string, path: string) =>
    request<{ commit: string; path: string; content: string; binary: boolean }>(
      'GET',
      `/apps/${app}/repos/${repo}/blob${q({ ref, path })}`,
    ),
  commits: (app: string, repo: string, ref: string) => request<Commit[]>('GET', `/apps/${app}/repos/${repo}/commits${q({ ref })}`),

  issues: (app: string, status = 'active') => request<Issue[]>('GET', `/apps/${app}/issues${q({ status })}`),
  issue: (app: string, n: number) => request<{ issue: Issue; links: LinkView[] }>('GET', `/apps/${app}/issues/${n}`),
  createIssue: (app: string, input: { title: string; body: string; assignee_id?: string; source?: Ref }) =>
    request<{ issue: Issue; links: LinkView[] }>('POST', `/apps/${app}/issues`, input),
  updateIssue: (app: string, n: number, patch: Partial<{ title: string; body: string; status: IssueStatus; assignee_id: string }>) =>
    request<{ issue: Issue; links: LinkView[] }>('PATCH', `/apps/${app}/issues/${n}`, patch),

  channels: (app?: string) => request<Channel[]>('GET', `/channels${q({ app })}`),
  createChannel: (name: string, app_key: string | null) => request<Channel>('POST', '/channels', { name, app_key }),
  channel: (id: string) => request<{ channel: Channel; issue_url?: string }>('GET', `/channels/${id}`),
  messages: (channel: string, thread?: string) => request<Message[]>('GET', `/channels/${channel}/messages${q({ thread })}`),
  postMessage: (channel: string, input: { body: string; thread_id?: string | null; code?: CodeInput }) =>
    request<Message>('POST', `/channels/${channel}/messages`, input),

  todos: () => request<Todo[]>('GET', '/todos'),
  createTodo: (input: { title?: string; source?: Ref }) => request<Todo>('POST', '/todos', input),
  updateTodo: (id: string, patch: { title?: string; done?: boolean }) => request<Todo>('PATCH', `/todos/${id}`, patch),
  deleteTodo: (id: string) => request<void>('DELETE', `/todos/${id}`),

  links: (ref: Ref) => request<LinkView[]>('GET', `/links${q({ type: ref.type, id: ref.id })}`),
}
