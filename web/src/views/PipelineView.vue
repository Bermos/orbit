<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, roleRank, type AgentRun, type Job, type LogChunk, type Pipeline, type Role } from '../api'
import { active, ago, duration, triggerText } from '../ci'
import StatusDot from '../components/StatusDot.vue'
import { subscribe } from '../realtime'
import { useToasts } from '../stores/toast'

const props = defineProps<{ app: string; repo: string; number: number }>()
const route = useRoute()
const router = useRouter()
const toasts = useToasts()
const pipeline = ref<Pipeline | null>(null)
const role = ref<Role>('none')
const error = ref('')
const busy = ref(false)
const now = ref(Date.now())
const timer = setInterval(() => (now.value = Date.now()), 1000)

const canDevelop = computed(() => roleRank[role.value] >= roleRank.developer)
// Without ?job, the first failed or running job, chosen once so the view does not jump.
const fallback = ref('')
const job = computed<Job | null>(() => {
  const jobs = pipeline.value?.jobs ?? []
  const name = (route.query.job as string | undefined) || fallback.value
  return jobs.find((j) => j.name === name) ?? jobs[0] ?? null
})

let unsubRepo: (() => void) | null = null
async function load() {
  error.value = ''
  try {
    const [p, d, a] = await Promise.all([
      api.pipeline(props.app, props.repo, props.number),
      api.repo(props.app, props.repo),
      api.app(props.app),
    ])
    pipeline.value = p
    role.value = a.my_role
    fallback.value = (p.jobs.find((j) => j.status === 'failure' || j.status === 'error') ?? p.jobs.find((j) => j.status === 'running'))?.name ?? ''
    unsubRepo?.()
    unsubRepo = subscribe('repo:' + d.repository.id, (type, data) => {
      if (type === 'pipeline.updated' && pipeline.value && (data as Pipeline).id === pipeline.value.id) pipeline.value = data
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
watch(() => [props.app, props.repo, props.number], load, { immediate: true })

async function cancel() {
  busy.value = true
  try {
    pipeline.value = await api.cancelPipeline(props.app, props.repo, props.number)
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
function select(j: Job) {
  router.replace({ query: { ...route.query, job: j.name } })
}

// Log of the selected job: existing chunks first, then live ones, deduped by seq.
const chunks = ref<LogChunk[]>([])
const logEl = ref<HTMLPreElement | null>(null)
const logLoading = ref(false)
let seen = new Set<number>()
let unsubJob: (() => void) | null = null
let logToken = 0

const ansi = /\x1b\[[0-9;?]*[A-Za-z]/g
const logText = computed(() => chunks.value.map((c) => c.data).join('').replace(ansi, ''))

async function addChunks(cs: LogChunk[]) {
  const el = logEl.value
  const stick = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 40
  for (const c of cs) {
    if (seen.has(c.seq)) continue
    seen.add(c.seq)
    const arr = chunks.value
    if (!arr.length || arr[arr.length - 1].seq < c.seq) arr.push(c)
    else arr.splice(arr.findIndex((x) => x.seq > c.seq), 0, c)
  }
  if (stick) {
    await nextTick()
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
  }
}

async function loadLog(id: string) {
  const token = ++logToken
  unsubJob?.()
  chunks.value = []
  seen = new Set()
  logLoading.value = true
  unsubJob = subscribe('job:' + id, (type, data) => {
    if (type === 'job.log' && token === logToken) addChunks([data as LogChunk])
  })
  try {
    let after = 0
    for (;;) {
      const res = await api.jobLogs(id, after)
      if (token !== logToken) return
      await addChunks(res.chunks)
      if (res.chunks.length < 2000) break
      after = res.chunks[res.chunks.length - 1].seq
    }
  } catch (e) {
    if (token === logToken) toasts.error(e)
  } finally {
    if (token === logToken) logLoading.value = false
  }
}
watch(() => job.value?.id, (id) => id && loadLog(id), { immediate: true })

// Agent jobs: the conversation the run answers in and the changes it made.
const run = ref<AgentRun | null>(null)
const patch = ref<string | null>(null)
watch(
  () => job.value?.agent_run_id,
  async (id) => {
    run.value = null
    if (id) run.value = (await api.agentRun(id).catch(() => null))?.run ?? null
  },
  { immediate: true },
)
watch(
  () => [job.value?.id, job.value?.has_patch] as const,
  async ([id, has]) => {
    patch.value = null
    if (id && has) patch.value = await api.jobPatch(id).catch(() => null)
  },
  { immediate: true },
)
const patchLines = computed(() =>
  (patch.value ?? '').split('\n').map((l) => ({
    text: l,
    cls: /^(diff --git|index |--- |\+\+\+ |new file|deleted file|similarity|rename )/.test(l)
      ? 'meta'
      : l.startsWith('@@')
        ? 'hunk'
        : l.startsWith('+')
          ? 'add'
          : l.startsWith('-')
            ? 'del'
            : '',
  })),
)

onUnmounted(() => {
  unsubRepo?.()
  unsubJob?.()
  logToken++
  clearInterval(timer)
})
</script>

<template>
  <div class="page wide">
    <div v-if="error" class="card pad error">{{ error }}</div>
    <template v-else-if="pipeline">
      <div class="page-header">
        <div>
          <div class="crumbs small muted">
            <RouterLink :to="`/apps/${app}`">{{ app }}</RouterLink> /
            <RouterLink :to="`/apps/${app}/repos/${repo}`">{{ repo }}</RouterLink> /
            <RouterLink :to="`/apps/${app}/repos/${repo}/pipelines`">pipelines</RouterLink> /
          </div>
          <h1 class="row">
            <StatusDot :status="pipeline.status" />
            Pipeline #{{ pipeline.number }}
            <span class="badge" :class="`st-${pipeline.status}`">{{ pipeline.status }}</span>
          </h1>
          <div class="row small wrap">
            <span class="mono">⎇ {{ pipeline.ref }}</span>
            <span class="muted">·</span>
            <RouterLink class="mono" :to="`/apps/${app}/repos/${repo}/tree/${pipeline.commit}`">{{ pipeline.commit.slice(0, 8) }}</RouterLink>
            <span class="muted">·</span>
            <span class="muted">{{ triggerText(pipeline) }}</span>
            <span class="muted">·</span>
            <span class="muted" :title="new Date(pipeline.created_at).toLocaleString()">{{ ago(pipeline.created_at, now) }}</span>
            <template v-if="pipeline.jobs.some((j) => j.started_at)">
              <span class="muted">·</span>
              <span class="muted">{{ duration(pipeline.created_at, pipeline.finished_at, now) }}</span>
            </template>
          </div>
          <p v-if="pipeline.error" class="error">{{ pipeline.error }}</p>
        </div>
        <span class="spacer" />
        <button v-if="canDevelop && active(pipeline.status)" class="small" :disabled="busy" @click="cancel">Cancel</button>
      </div>

      <div class="cols">
        <div class="card jobs">
          <ul class="list">
            <li v-for="j in pipeline.jobs" :key="j.id" :class="{ sel: job?.id === j.id }" @click="select(j)">
              <StatusDot :status="j.status" />
              <div class="jtext">
                <div class="row">
                  <span class="jname">{{ j.name }}</span>
                  <span v-if="j.kind === 'agent'" class="badge accent">agent</span>
                </div>
                <div class="small muted">
                  {{ j.status }}<template v-if="j.started_at"> · {{ duration(j.started_at, j.finished_at, now) }}</template>
                  <template v-if="j.needs.length"> · needs {{ j.needs.join(', ') }}</template>
                </div>
              </div>
            </li>
          </ul>
        </div>

        <section v-if="job" class="detail">
          <div class="row">
            <h2 class="grow">{{ job.name }}</h2>
            <span v-if="job.exit_code !== null" class="small muted">exit code {{ job.exit_code }}</span>
            <span v-if="job.runner" class="small muted">on {{ job.runner }}</span>
          </div>
          <p v-if="job.error" class="error">{{ job.error }}</p>
          <div v-if="job.kind === 'agent'" class="card pad agent">
            <div class="row small">
              <span class="badge accent">@{{ job.spec.agent }}</span>
              <span class="spacer" />
              <RouterLink
                v-if="run?.channel_id"
                :to="{ path: `/channels/${run.channel_id}`, query: run.reply_message_id ? { message: run.reply_message_id } : {} }"
              >
                Open conversation
              </RouterLink>
            </div>
            <div v-if="job.spec.prompt" class="prompt small">{{ job.spec.prompt }}</div>
          </div>
          <pre ref="logEl" class="log">{{ logText }}<span v-if="logLoading" class="dim">Loading…</span><span v-else-if="!logText" class="dim">{{ active(job.status) ? 'Waiting for output…' : 'No output.' }}</span></pre>
          <template v-if="job.has_patch">
            <h3>Changes</h3>
            <div class="card diff">
              <div v-if="patch === null" class="empty">Loading…</div>
              <div v-else-if="!patch" class="empty">No changes.</div>
              <pre v-else><span v-for="(l, i) in patchLines" :key="i" :class="l.cls">{{ l.text || ' ' }}</span></pre>
            </div>
          </template>
        </section>
        <div v-else class="card empty">This pipeline has no jobs.</div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.wide { max-width: 1300px; }
h1.row { gap: 10px; }
.grow { flex: 1; }
.cols { display: grid; grid-template-columns: 280px 1fr; gap: 20px; align-items: start; }
@media (max-width: 900px) { .cols { grid-template-columns: 1fr; } }
.jobs li { cursor: pointer; }
.jobs li:hover { background: var(--surface-2); }
.jobs li.sel { background: var(--accent-soft); }
.jtext { min-width: 0; flex: 1; }
.jname { font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.detail { min-width: 0; }
.detail h2 { margin-top: 0; }
.agent { margin-bottom: 10px; }
.prompt { margin-top: 8px; white-space: pre-wrap; color: var(--muted); max-height: 140px; overflow-y: auto; }
.log {
  background: #14141f; color: #dcdce8; border-radius: var(--radius); padding: 12px 14px; margin: 0;
  max-height: 65vh; min-height: 120px; overflow: auto; white-space: pre-wrap; word-break: break-all; line-height: 1.45;
}
.dim { color: #8a8aa0; }
.diff { overflow-x: auto; }
.diff pre { margin: 0; padding: 8px 0; }
.diff span { display: block; padding: 0 12px; white-space: pre; }
.diff .add { background: color-mix(in srgb, var(--success) 15%, transparent); }
.diff .del { background: color-mix(in srgb, var(--danger) 15%, transparent); }
.diff .hunk { color: var(--accent); background: var(--accent-soft); }
.diff .meta { color: var(--muted); font-weight: 600; }
.st-success { color: var(--success); }
.st-failure, .st-error { color: var(--danger); }
.st-running { color: var(--accent); }
.st-pending { color: var(--warning); }
</style>
