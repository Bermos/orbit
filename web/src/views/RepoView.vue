<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Commit, type RepoDetail, type TreeEntry } from '../api'
import CommitList from '../components/CommitList.vue'
import DiscussCodeDialog from '../components/DiscussCodeDialog.vue'
import NewMergeRequestDialog from '../components/NewMergeRequestDialog.vue'
import RepoHeader from '../components/RepoHeader.vue'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'

const props = defineProps<{ app: string; repo: string; mode: string; gitRef: string; path: string }>()
const route = useRoute()
const router = useRouter()
const toasts = useToasts()
const todos = useTodos()

const detail = ref<RepoDetail | null>(null)
const entries = ref<TreeEntry[]>([])
const commits = ref<Commit[]>([])
const content = ref<string | null>(null)
const binary = ref(false)
const commit = ref('')
const error = ref('')
const selStart = ref<number | null>(null)
const selEnd = ref<number | null>(null)
const discussing = ref(false)
const openingMR = ref(false)

const showCommits = computed(() => props.mode === 'tree' && route.query.tab === 'commits')
const ref_ = computed(() => props.gitRef || detail.value?.repository.default_branch || 'main')
// Viewing a branch other than the default one offers to open a merge request.
const featureBranch = computed(
  () => detail.value && ref_.value !== detail.value.repository.default_branch && detail.value.branches.some((b) => b.name === ref_.value),
)
const lines = computed(() => {
  if (content.value === null) return []
  const ls = content.value.split('\n')
  if (ls.length && ls[ls.length - 1] === '') ls.pop()
  return ls
})
const crumbs = computed(() => {
  const parts = props.path ? props.path.split('/') : []
  return parts.map((p, i) => ({ name: p, path: parts.slice(0, i + 1).join('/') }))
})
const selection = computed(() => {
  if (selStart.value === null) return null
  const a = selStart.value
  const b = selEnd.value ?? a
  return { start: Math.min(a, b), end: Math.max(a, b) }
})

function base() {
  return `/apps/${props.app}/repos/${props.repo}`
}
function treeLink(path: string) {
  return `${base()}/tree/${ref_.value}/${path}`
}
function blobLink(path: string) {
  return `${base()}/blob/${ref_.value}/${path}`
}

async function load() {
  error.value = ''
  try {
    if (!detail.value || detail.value.repository.name !== props.repo) detail.value = await api.repo(props.app, props.repo)
    if (detail.value.empty) return
    if (props.mode === 'blob') {
      const b = await api.blob(props.app, props.repo, ref_.value, props.path)
      content.value = b.content
      binary.value = b.binary
      commit.value = b.commit
      applyLinesQuery()
    } else {
      content.value = null
      const [t, c] = await Promise.all([
        api.tree(props.app, props.repo, ref_.value, props.path),
        props.path && !showCommits.value ? Promise.resolve(commits.value) : api.commits(props.app, props.repo, ref_.value),
      ])
      entries.value = t.entries.sort((x, y) => (x.type === y.type ? x.name.localeCompare(y.name) : x.type === 'tree' ? -1 : 1))
      commit.value = t.commit
      commits.value = c
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
watch(() => [props.app, props.repo, props.mode, props.gitRef, props.path, showCommits.value], load, { immediate: true })

async function applyLinesQuery() {
  const m = /^(\d+)(?:-(\d+))?$/.exec((route.query.lines as string) ?? '')
  if (!m) {
    selStart.value = selEnd.value = null
    return
  }
  selStart.value = Number(m[1])
  selEnd.value = Number(m[2] ?? m[1])
  await nextTick()
  document.getElementById(`L${selStart.value}`)?.scrollIntoView({ block: 'center' })
}
watch(() => route.query.lines, () => props.mode === 'blob' && applyLinesQuery())

function clickLine(n: number, e: MouseEvent) {
  if (e.shiftKey && selStart.value !== null) selEnd.value = n
  else {
    selStart.value = n
    selEnd.value = n
  }
  const s = selection.value!
  router.replace({ query: { ...route.query, lines: s.start === s.end ? `${s.start}` : `${s.start}-${s.end}` } })
}
function clearSelection() {
  const query = { ...route.query }
  delete query.lines
  router.replace({ query })
}

const codeInput = computed(() =>
  selection.value
    ? { app: props.app, repo: props.repo, ref: commit.value, path: props.path, start: selection.value.start, end: selection.value.end }
    : null,
)
const selectedLines = computed(() => (selection.value ? lines.value.slice(selection.value.start - 1, selection.value.end) : []))

async function todoForLines() {
  const s = selection.value!
  try {
    await todos.add({
      title: `Look at ${props.path}:${s.start}-${s.end}`,
      source: { type: 'code', id: `${props.app}/${props.repo}@${commit.value}:${props.path}#L${s.start}-L${s.end}` },
    })
    toasts.show('Added to your to-dos', { link: '/todos' })
  } catch (e) {
    toasts.error(e)
  }
}
async function copyLink() {
  const s = selection.value!
  const url = `${location.origin}${base()}/blob/${commit.value}/${props.path}?lines=${s.start}-${s.end}`
  await navigator.clipboard?.writeText(url)
  toasts.show('Permalink copied')
}
function switchBranch(e: Event) {
  const b = (e.target as HTMLSelectElement).value
  if (showCommits.value) router.push({ path: `${base()}/tree/${b}`, query: { tab: 'commits' } })
  else router.push(props.mode === 'blob' ? `${base()}/blob/${b}/${props.path}` : `${base()}/tree/${b}/${props.path}`)
}
</script>

<template>
  <div class="page wide">
    <RepoHeader :app="app" :repo="repo" :detail="detail" :tab="showCommits ? 'commits' : 'code'" :git-ref="gitRef" />

    <div v-if="detail?.empty" class="card pad">
      <h3 style="margin-top: 0">This repository is empty</h3>
      <p class="muted">Push an existing repository with your Orbit username and password:</p>
      <pre class="cmd">git remote add orbit {{ detail.clone_url }}
git push orbit {{ detail.repository.default_branch }}</pre>
    </div>

    <template v-else-if="detail">
      <div class="row bar">
        <select class="branch" :value="ref_" aria-label="Branch" @change="switchBranch">
          <option v-for="b in detail.branches" :key="b.name" :value="b.name">⎇ {{ b.name }}</option>
          <option v-if="!detail.branches.some((b) => b.name === ref_)" :value="ref_">{{ ref_.slice(0, 12) }}</option>
        </select>
        <button v-if="featureBranch" class="small" @click="openingMR = true">⇄ New merge request</button>
        <div v-if="!showCommits" class="path mono">
          <RouterLink :to="treeLink('')">{{ repo }}</RouterLink>
          <template v-for="(c, i) in crumbs" :key="c.path">
            /
            <RouterLink v-if="i < crumbs.length - 1 || mode === 'tree'" :to="treeLink(c.path)">{{ c.name }}</RouterLink>
            <strong v-else>{{ c.name }}</strong>
          </template>
        </div>
      </div>

      <div v-if="error" class="card pad error">{{ error }}</div>

      <div v-else-if="showCommits" class="card">
        <CommitList v-if="commits.length" :app="app" :repo="repo" :repo-id="detail.repository.id" :commits="commits" />
        <div v-else class="empty">No commits.</div>
      </div>

      <div v-else-if="mode === 'tree'" class="grid2">
        <div class="card">
          <ul class="list">
            <li v-if="path">
              <RouterLink :to="treeLink(crumbs.slice(0, -1).map((c) => c.name).join('/'))">..</RouterLink>
            </li>
            <li v-for="e in entries" :key="e.path">
              <span class="icon">{{ e.type === 'tree' ? '📁' : '📄' }}</span>
              <RouterLink :to="e.type === 'tree' ? treeLink(e.path) : blobLink(e.path)" class="mono">{{ e.name }}</RouterLink>
            </li>
          </ul>
        </div>
        <aside v-if="commits.length">
          <div class="row">
            <h3 class="grow" style="margin-top: 0">Recent commits</h3>
            <RouterLink class="small" :to="{ path: `${base()}/tree/${ref_}`, query: { tab: 'commits' } }" style="margin-top: -8px">All commits</RouterLink>
          </div>
          <div class="card">
            <CommitList :app="app" :repo="repo" :repo-id="detail.repository.id" :commits="commits.slice(0, 10)" compact />
          </div>
        </aside>
      </div>

      <div v-else class="card code">
        <div v-if="binary" class="empty">Binary or very large file, not shown.</div>
        <table v-else>
          <tbody>
            <tr
              v-for="(l, i) in lines"
              :id="`L${i + 1}`"
              :key="i"
              :class="{ sel: selection && i + 1 >= selection.start && i + 1 <= selection.end }"
            >
              <td class="ln" @click="clickLine(i + 1, $event)">{{ i + 1 }}</td>
              <td class="src">{{ l || ' ' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="mode === 'blob' && !binary && !selection" class="small muted">
        Click a line number to select it, shift-click to select a range, then discuss it or add a to-do.
      </p>
    </template>

    <div v-if="selection && mode === 'blob'" class="selbar card">
      <span>Lines {{ selection.start }}–{{ selection.end }} selected</span>
      <button class="primary small" @click="discussing = true">💬 Discuss</button>
      <button class="small" @click="todoForLines">+ To-do</button>
      <button class="small" @click="copyLink">Copy permalink</button>
      <button class="ghost small" aria-label="Clear selection" @click="clearSelection">✕</button>
    </div>
    <NewMergeRequestDialog v-if="openingMR" :app="app" :repo="repo" :source="ref_" @close="openingMR = false" />
    <DiscussCodeDialog v-if="discussing && codeInput" :code="codeInput" :lines="selectedLines" @close="discussing = false" />
  </div>
</template>

<style scoped>
.wide { max-width: 1300px; }
.cmd { background: var(--surface-2); padding: 12px; border-radius: 6px; }
.bar { margin-bottom: 12px; }
.branch { width: auto; }
.path { font-size: 13.5px; }
.icon { width: 18px; }
.code { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-family: var(--mono); font-size: 12.5px; tab-size: 4; }
td { padding: 0 12px; line-height: 20px; vertical-align: top; }
.ln { width: 1%; text-align: right; color: var(--muted); user-select: none; cursor: pointer; border-right: 1px solid var(--border); }
.ln:hover { color: var(--accent); }
.src { white-space: pre; }
tr.sel td { background: var(--accent-soft); }
tr.sel .ln { color: var(--accent); font-weight: 600; }
.selbar {
  position: fixed; bottom: 24px; left: 50%; transform: translateX(calc(-50% + 120px));
  display: flex; gap: 8px; align-items: center; padding: 8px 12px; box-shadow: 0 8px 30px #0003; z-index: 20;
}
.grow { flex: 1; }
</style>
