<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, type Check, type Commit, type DiffFile, type MergeRequest, type MergeRequestDetail, type Message, type MovedComment } from '../api'
import { ago, internalPath } from '../ci'
import { subscribe } from '../realtime'
import ChatPanel from '../components/ChatPanel.vue'
import CommitList from '../components/CommitList.vue'
import CreateIssueDialog from '../components/CreateIssueDialog.vue'
import DiffView from '../components/DiffView.vue'
import LinkList from '../components/LinkList.vue'
import LinksDialog from '../components/LinksDialog.vue'
import StatusDot from '../components/StatusDot.vue'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'

const props = defineProps<{ app: string; number: number }>()
const route = useRoute()
const session = useSession()
const toasts = useToasts()
const todos = useTodos()

const detail = ref<MergeRequestDetail | null>(null)
const files = ref<DiffFile[] | null>(null)
const moved = ref<MovedComment[]>([])
const commits = ref<Commit[] | null>(null)
const discussion = ref<Message[]>([])
const editing = ref(false)
const draft = ref({ title: '', body: '' })
const strategy = ref<'merge' | 'squash'>('merge')
const deleteBranch = ref(true)
const busy = ref(false)
const issueSource = ref<Message | null>(null)
const linksFor = ref<Message | null>(null)

const tab = computed(() => (route.query.tab as string) || 'overview')
const mr = computed(() => detail.value?.merge_request ?? null)
const comments = computed(() => discussion.value.filter((m) => m.attachments.some((a) => a.type === 'code')))
const myReview = computed(() => detail.value?.reviews.find((r) => r.user.id === session.user?.id))
// Each inline comment links the merge request to its code range; those are
// shown on the diff, not in the list.
const otherLinks = computed(() => detail.value?.links.filter((l) => l.entity.type !== 'code') ?? [])

let unsubs: (() => void)[] = []
async function load() {
  const d = await api.mergeRequest(props.app, props.number)
  const headChanged = d.merge_request.head_sha !== detail.value?.merge_request.head_sha
  detail.value = d
  if (headChanged) {
    files.value = commits.value = null
    loadTab()
  }
}
async function loadDiscussion() {
  if (mr.value) discussion.value = await api.messages(mr.value.channel_id)
}
async function loadTab() {
  if (!mr.value) return
  if (tab.value === 'changes' && !files.value) {
    const d = await api.mergeRequestDiff(props.app, props.number)
    moved.value = d.moved
    files.value = d.files
  }
  if (tab.value === 'commits' && !commits.value) commits.value = await api.mergeRequestCommits(props.app, props.number)
}

function onMessage(type: string, m: Message) {
  if (!mr.value || m.channel_id !== mr.value.channel_id || m.thread_id) return
  const i = discussion.value.findIndex((x) => x.id === m.id)
  if (i >= 0) discussion.value[i] = m
  else if (type === 'message.created') discussion.value.push(m)
}

watch(
  () => [props.app, props.number],
  async () => {
    detail.value = null
    files.value = commits.value = null
    unsubs.forEach((u) => u())
    await load()
    const m = mr.value!
    unsubs = [
      subscribe('channel:' + m.channel_id, onMessage),
      subscribe('repo:' + m.repo_id, (type, data) => {
        if (type === 'merge_request.updated' && (data as MergeRequest).id === mr.value?.id) load()
        if (type === 'check.updated' && (data as Check).commit === mr.value?.head_sha) load()
      }),
    ]
    if (session.user) unsubs.push(subscribe('user:' + session.user.id, (type, data) => type.startsWith('message.') && onMessage(type, data)))
    await loadDiscussion()
  },
  { immediate: true },
)
watch(tab, loadTab)
onBeforeUnmount(() => unsubs.forEach((u) => u()))

async function act(fn: () => Promise<MergeRequestDetail>, done?: string) {
  busy.value = true
  try {
    detail.value = await fn()
    if (done) toasts.show(done)
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
const update = (patch: Parameters<typeof api.updateMergeRequest>[2]) => act(() => api.updateMergeRequest(props.app, props.number, patch))
const review = (state: 'approved' | 'changes_requested' | '') => act(() => api.reviewMergeRequest(props.app, props.number, state))
const merge = (force = false) =>
  act(() => api.mergeMergeRequest(props.app, props.number, { strategy: strategy.value, delete_source_branch: deleteBranch.value, force }), 'Merged')

function startEdit() {
  draft.value = { title: mr.value!.title, body: mr.value!.body }
  editing.value = true
}
async function saveEdit() {
  await update(draft.value)
  editing.value = false
}
async function addTodo() {
  try {
    await todos.add({ title: `Review ${props.app}!${props.number}: ${mr.value!.title}`, source: { type: 'merge_request', id: mr.value!.id } })
    toasts.show('Added to your to-dos', { link: '/todos' })
  } catch (e) {
    toasts.error(e)
  }
}
async function runPipeline() {
  try {
    const p = await api.runPipeline(props.app, mr.value!.repo, mr.value!.source_branch)
    toasts.show(`Started pipeline #${p.number}`, { link: `/apps/${props.app}/repos/${mr.value!.repo}/pipelines/${p.number}` })
  } catch (e) {
    toasts.error(e)
  }
}
const branchLink = (b: string) => `/apps/${props.app}/repos/${mr.value!.repo}/tree/${b}`
const short = (sha: string | null) => (sha ?? '').slice(0, 8)
</script>

<template>
  <div v-if="detail && mr" class="mr">
    <div class="top">
      <div class="crumbs small muted">
        <RouterLink :to="`/apps/${app}`">{{ app }}</RouterLink> /
        <RouterLink :to="`/apps/${app}/repos/${mr.repo}`">{{ mr.repo }}</RouterLink> /
        <RouterLink :to="`/apps/${app}/repos/${mr.repo}/merge-requests`">merge requests</RouterLink> / !{{ mr.number }}
      </div>
      <div v-if="!editing" class="head">
        <h1>
          {{ mr.title }} <span class="muted">!{{ mr.number }}</span>
        </h1>
        <div class="row">
          <button class="small" @click="addTodo">+ To-do</button>
          <button v-if="detail.permissions.edit" class="small" @click="startEdit">Edit</button>
          <template v-if="detail.permissions.edit && mr.status !== 'merged'">
            <button v-if="mr.status === 'open'" class="small" :disabled="busy" @click="update({ draft: !mr.draft })">
              {{ mr.draft ? 'Mark ready' : 'Mark as draft' }}
            </button>
            <button v-if="mr.status === 'open'" class="small" :disabled="busy" @click="update({ status: 'closed' })">Close</button>
            <button v-else class="small" :disabled="busy" @click="update({ status: 'open' })">Reopen</button>
          </template>
        </div>
      </div>
      <form v-else class="stack edit" @submit.prevent="saveEdit">
        <input v-model="draft.title" required aria-label="Title" />
        <textarea v-model="draft.body" rows="5" aria-label="Description" />
        <div class="row">
          <button class="primary" type="submit">Save</button>
          <button type="button" @click="editing = false">Cancel</button>
        </div>
      </form>
      <div class="meta row wrap small">
        <span class="state" :class="mr.status">{{ mr.draft && mr.status === 'open' ? 'Draft' : mr.status }}</span>
        <span>
          {{ mr.author?.display_name ?? 'Someone' }} {{ mr.status === 'open' ? 'wants to merge' : 'asked to merge' }}
          <RouterLink class="branch mono" :to="branchLink(mr.source_branch)">{{ mr.source_branch }}</RouterLink>
          into
          <RouterLink class="branch mono" :to="branchLink(mr.target_branch)">{{ mr.target_branch }}</RouterLink>
        </span>
        <span class="muted">· opened {{ ago(mr.created_at) }}</span>
      </div>
    </div>

    <nav class="tabs">
      <RouterLink :to="{ query: {} }" :class="{ active: tab === 'overview' }">Overview</RouterLink>
      <RouterLink :to="{ query: { tab: 'changes' } }" :class="{ active: tab === 'changes' }">
        Changes <span v-if="comments.length" class="badge">{{ comments.length }} 💬</span>
      </RouterLink>
      <RouterLink :to="{ query: { tab: 'commits' } }" :class="{ active: tab === 'commits' }">Commits</RouterLink>
    </nav>

    <div class="cols">
      <div class="left">
        <template v-if="tab === 'overview'">
          <div v-if="mr.body && !editing" class="card pad body">{{ mr.body }}</div>
          <h3 class="disc">Discussion</h3>
          <div class="discussion card">
            <ChatPanel :channel-id="mr.channel_id" :app-key="app" empty-text="No comments yet. Comment on lines in Changes, or here." />
          </div>
        </template>
        <template v-else-if="tab === 'changes'">
          <div v-if="!files" class="card empty">Loading…</div>
          <DiffView
            v-else
            :mr="mr"
            :files="files"
            :moved="moved"
            :comments="comments"
            :can-comment="true"
            @issue="issueSource = $event"
            @links="linksFor = $event"
          />
        </template>
        <div v-else class="card">
          <div v-if="!commits" class="empty">Loading…</div>
          <CommitList v-else :app="app" :repo="mr.repo" :repo-id="mr.repo_id" :commits="commits" />
        </div>
      </div>

      <aside class="side">
        <section class="card pad merge" :class="mr.status">
          <template v-if="mr.status === 'merged'">
            <strong>✓ Merged</strong>
            <div class="small muted">
              by {{ mr.merged_by?.display_name ?? 'someone' }} {{ mr.merged_at ? ago(mr.merged_at) : '' }} into {{ mr.target_branch }}
              <template v-if="mr.merge_commit_sha">
                as <RouterLink class="mono" :to="`/apps/${app}/repos/${mr.repo}/tree/${mr.merge_commit_sha}`">{{ short(mr.merge_commit_sha) }}</RouterLink>
              </template>
            </div>
          </template>
          <template v-else-if="mr.status === 'closed'">
            <strong>Closed</strong>
            <div class="small muted">Closed {{ mr.closed_at ? ago(mr.closed_at) : '' }} without merging.</div>
          </template>
          <template v-else>
            <div v-if="detail.merge_status.reason" class="blocker">{{ detail.merge_status.reason }}</div>
            <div v-else-if="detail.merge_status.conflicts.length" class="blocker">
              Conflicts with {{ mr.target_branch }} in:
              <ul>
                <li v-for="c in detail.merge_status.conflicts" :key="c" class="mono small">{{ c }}</li>
              </ul>
              <span class="small muted">Merge {{ mr.target_branch }} into {{ mr.source_branch }} and resolve them, then push.</span>
            </div>
            <template v-else>
              <div v-for="o in detail.merge_status.overridable" :key="o" class="warn small">⚠ {{ o }}</div>
              <div v-if="!detail.merge_status.overridable.length" class="ok small">✓ Ready to merge</div>
              <template v-if="detail.permissions.merge">
                <select v-model="strategy" aria-label="Merge method" class="small-select">
                  <option value="merge">Merge commit</option>
                  <option value="squash">Squash into one commit</option>
                </select>
                <label class="check small"><input v-model="deleteBranch" type="checkbox" /> Delete {{ mr.source_branch }} after merging</label>
                <button v-if="!detail.merge_status.overridable.length" class="primary" :disabled="busy" @click="merge()">Merge</button>
                <button v-else-if="detail.permissions.force" class="danger" :disabled="busy" @click="merge(true)">Merge anyway</button>
                <div v-else class="small muted">A maintainer can merge anyway.</div>
              </template>
              <div v-else class="small muted">Merging needs the developer role.</div>
            </template>
          </template>
        </section>

        <section>
          <h4>Checks <span class="mono small muted">@ {{ short(mr.head_sha) }}</span></h4>
          <ul v-if="detail.checks.length" class="plain">
            <li v-for="c in detail.checks" :key="c.id" class="row">
              <StatusDot :status="c.status" />
              <span class="name">{{ c.name }}</span>
              <span class="spacer" />
              <template v-if="c.details_url">
                <RouterLink v-if="internalPath(c.details_url)" :to="internalPath(c.details_url)!" class="small">Details</RouterLink>
                <a v-else :href="c.details_url" target="_blank" rel="noopener" class="small">Details ↗</a>
              </template>
            </li>
          </ul>
          <div v-else class="small muted">
            No checks on this commit.
            <button v-if="detail.permissions.merge && mr.status === 'open'" class="ghost small" @click="runPipeline">Run pipeline</button>
          </div>
        </section>

        <section>
          <h4>Reviews</h4>
          <ul v-if="detail.reviews.length" class="plain">
            <li v-for="r in detail.reviews" :key="r.user.id" class="row">
              <span :class="r.state">{{ r.state === 'approved' ? '✓' : '✗' }}</span>
              <span>{{ r.user.display_name }}</span>
              <span class="small muted">{{ r.state === 'approved' ? 'approved' : 'requested changes' }}</span>
              <span v-if="r.outdated" class="badge small" title="Reviewed an earlier version">earlier version</span>
            </li>
          </ul>
          <div v-else class="small muted">No reviews yet.</div>
          <div v-if="detail.permissions.review && mr.status === 'open'" class="row wrap review-actions">
            <button class="small" :disabled="busy" @click="review('approved')">✓ Approve</button>
            <button class="small" :disabled="busy" @click="review('changes_requested')">Request changes</button>
            <button v-if="myReview" class="ghost small" :disabled="busy" @click="review('')">Withdraw</button>
          </div>
        </section>

        <section>
          <h4>Linked</h4>
          <LinkList :links="otherLinks" />
        </section>
      </aside>
    </div>
    <CreateIssueDialog v-if="issueSource" :source="issueSource" :default-app="app" @close="issueSource = null" />
    <LinksDialog v-if="linksFor" :target="{ type: 'message', id: linksFor.id }" @close="linksFor = null" />
  </div>
</template>

<style scoped>
.mr { padding: 24px 32px; display: flex; flex-direction: column; min-height: 100%; }
.head { display: flex; align-items: flex-start; gap: 16px; }
.head h1 { flex: 1; }
.edit { max-width: 760px; }
.meta { margin: 6px 0 12px; }
.state { text-transform: capitalize; font-weight: 600; padding: 1px 10px; border-radius: 999px; background: var(--accent-soft); color: var(--accent); }
.state.merged { background: color-mix(in srgb, var(--success) 18%, transparent); color: var(--success); }
.state.closed { background: var(--surface-2); color: var(--muted); }
.branch { background: var(--surface-2); padding: 0 6px; border-radius: 4px; font-size: 12px; }
.cols { display: grid; grid-template-columns: minmax(0, 1fr) 280px; gap: 24px; flex: 1; }
.left { display: flex; flex-direction: column; min-width: 0; }
.body { white-space: pre-wrap; margin-bottom: 8px; max-height: 30vh; overflow-y: auto; }
.disc { margin: 8px 0; }
.discussion { height: 560px; overflow: hidden; }
.side { display: flex; flex-direction: column; gap: 18px; }
.side h4 { margin: 0 0 6px; font-size: 13px; }
.merge { display: flex; flex-direction: column; gap: 8px; border-left: 3px solid var(--accent); }
.merge.merged { border-left-color: var(--success); }
.merge.closed { border-left-color: var(--border); }
.blocker { color: var(--danger); font-size: 13px; }
.blocker ul { margin: 4px 0; padding-left: 18px; }
.warn { color: var(--warning); }
.ok { color: var(--success); font-weight: 600; }
.small-select { padding: 4px 8px; font-size: 13px; }
.check { display: flex; gap: 6px; align-items: center; font-weight: normal; margin: 0; }
.check input { width: auto; margin: 0; }
button.danger { background: var(--danger); border-color: var(--danger); color: white; }
.plain { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
.name { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.approved { color: var(--success); font-weight: 700; }
.changes_requested { color: var(--danger); font-weight: 700; }
.review-actions { margin-top: 8px; }
@media (max-width: 1000px) { .cols { grid-template-columns: 1fr; } }
</style>
