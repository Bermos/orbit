<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, roleRank, type Pipeline, type RepoDetail, type Role } from '../api'
import { ago, triggerText } from '../ci'
import RepoHeader from '../components/RepoHeader.vue'
import StatusDot from '../components/StatusDot.vue'
import { subscribe } from '../realtime'
import { useToasts } from '../stores/toast'

const props = defineProps<{ app: string; repo: string }>()
const router = useRouter()
const toasts = useToasts()
const detail = ref<RepoDetail | null>(null)
const role = ref<Role>('none')
const pipelines = ref<Pipeline[]>([])
const branch = ref('')
const busy = ref(false)
const error = ref('')
const now = ref(Date.now())
const timer = setInterval(() => (now.value = Date.now()), 30000)

const canRun = computed(() => roleRank[role.value] >= roleRank.developer && detail.value && !detail.value.empty)

let unsub: (() => void) | null = null
async function load() {
  error.value = ''
  try {
    const [d, a, ps] = await Promise.all([api.repo(props.app, props.repo), api.app(props.app), api.pipelines(props.app, props.repo)])
    detail.value = d
    role.value = a.my_role
    pipelines.value = ps
    branch.value = d.repository.default_branch
    unsub?.()
    unsub = subscribe('repo:' + d.repository.id, (type, data) => {
      if (type !== 'pipeline.updated') return
      const p = data as Pipeline
      const i = pipelines.value.findIndex((x) => x.id === p.id)
      if (i >= 0) pipelines.value[i] = p
      else pipelines.value.unshift(p)
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
watch(() => [props.app, props.repo], load, { immediate: true })
onUnmounted(() => {
  unsub?.()
  clearInterval(timer)
})

async function run() {
  busy.value = true
  try {
    const p = await api.runPipeline(props.app, props.repo, branch.value)
    router.push(`/apps/${props.app}/repos/${props.repo}/pipelines/${p.number}`)
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page wide">
    <RepoHeader :app="app" :repo="repo" :detail="detail" tab="pipelines" />
    <div v-if="error" class="card pad error">{{ error }}</div>
    <template v-else-if="detail">
      <div v-if="canRun" class="row bar">
        <span class="spacer" />
        <select v-model="branch" class="branch" aria-label="Branch">
          <option v-for="b in detail.branches" :key="b.name" :value="b.name">⎇ {{ b.name }}</option>
        </select>
        <button class="primary" :disabled="busy || !branch" @click="run">Run pipeline</button>
      </div>
      <div class="card">
        <ul v-if="pipelines.length" class="list">
          <li v-for="p in pipelines" :key="p.id">
            <StatusDot :status="p.status" />
            <RouterLink :to="`/apps/${app}/repos/${repo}/pipelines/${p.number}`" class="num">#{{ p.number }}</RouterLink>
            <span class="badge" :class="`st-${p.status}`">{{ p.status }}</span>
            <span class="mono small">⎇ {{ p.ref }}</span>
            <RouterLink class="mono small" :to="`/apps/${app}/repos/${repo}/tree/${p.commit}`">{{ p.commit.slice(0, 8) }}</RouterLink>
            <span class="small muted">{{ triggerText(p) }}</span>
            <span class="spacer" />
            <span class="jobs">
              <StatusDot v-for="j in p.jobs" :key="j.id" :status="j.status" :title="`${j.name}: ${j.status}`" />
            </span>
            <span class="small muted when" :title="new Date(p.created_at).toLocaleString()">{{ ago(p.created_at, now) }}</span>
          </li>
        </ul>
        <div v-else class="empty">
          No pipelines yet. Add a <span class="mono">.orbit/pipeline.yml</span> to the repository and push, or run one manually.
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.wide { max-width: 1300px; }
.bar { margin-bottom: 12px; }
.branch { width: auto; }
.num { font-weight: 600; min-width: 36px; }
.jobs { display: inline-flex; gap: 3px; }
.when { min-width: 80px; text-align: right; }
.st-success { color: var(--success); }
.st-failure, .st-error { color: var(--danger); }
.st-running { color: var(--accent); }
.st-pending { color: var(--warning); }
</style>
