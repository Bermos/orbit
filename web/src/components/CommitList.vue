<script setup lang="ts">
import { onUnmounted, reactive, ref, watch } from 'vue'
import { api, type Check, type Commit } from '../api'
import { internalPath } from '../ci'
import { subscribe } from '../realtime'
import StatusDot from './StatusDot.vue'

const props = defineProps<{ app: string; repo: string; repoId: string; commits: Commit[]; compact?: boolean }>()
const open = ref<string | null>(null)
const checks = ref<Check[]>([])
const states = reactive<Record<string, string>>({})

async function toggle(sha: string) {
  if (open.value === sha) {
    open.value = null
    return
  }
  open.value = sha
  checks.value = []
  const res = await api.checks(props.app, props.repo, sha)
  if (open.value === sha) checks.value = res.checks
  states[sha] = res.state
}

let unsub: (() => void) | null = null
watch(
  () => props.repoId,
  (id) => {
    unsub?.()
    unsub = subscribe('repo:' + id, async (type, data) => {
      if (type !== 'check.updated') return
      const c = data as Check
      if (!props.commits.some((x) => x.sha === c.commit)) return
      if (open.value === c.commit) {
        const i = checks.value.findIndex((x) => x.id === c.id)
        if (i >= 0) checks.value[i] = c
        else checks.value.push(c)
      }
      states[c.commit] = (await api.checks(props.app, props.repo, c.commit)).state
    })
  },
  { immediate: true },
)
onUnmounted(() => unsub?.())
</script>

<template>
  <ul class="list">
    <li v-for="c in commits" :key="c.sha" class="commit">
      <div class="line">
        <div class="text">
          <div class="subject">{{ c.subject }}</div>
          <div class="small muted">
            {{ c.author }} · {{ compact ? new Date(c.date).toLocaleDateString() : new Date(c.date).toLocaleString() }} ·
            <RouterLink class="mono" :to="`/apps/${app}/repos/${repo}/tree/${c.sha}`">{{ c.sha.slice(0, 8) }}</RouterLink>
          </div>
        </div>
        <button v-if="states[c.sha] ?? c.checks_state" class="ghost small checks-btn" :title="`Checks: ${states[c.sha] ?? c.checks_state}`" @click="toggle(c.sha)">
          <StatusDot :status="states[c.sha] ?? c.checks_state" />
          <span v-if="!compact">Checks</span>
        </button>
      </div>
      <ul v-if="open === c.sha" class="checks">
        <li v-if="!checks.length" class="small muted">Loading…</li>
        <li v-for="k in checks" :key="k.id">
          <StatusDot :status="k.status" />
          <strong class="name">{{ k.name }}</strong>
          <span class="small muted summary">{{ k.summary || k.status }}</span>
          <template v-if="k.details_url">
            <RouterLink v-if="internalPath(k.details_url)" :to="internalPath(k.details_url)!" class="small">Details</RouterLink>
            <a v-else :href="k.details_url" target="_blank" rel="noopener" class="small">Details ↗</a>
          </template>
        </li>
      </ul>
    </li>
  </ul>
</template>

<style scoped>
.commit { flex-direction: column; align-items: stretch; }
.line { display: flex; gap: 10px; align-items: center; }
.text { flex: 1; min-width: 0; }
.subject { overflow: hidden; text-overflow: ellipsis; }
.checks-btn { color: var(--muted); }
.checks { list-style: none; margin: 6px 0 0; padding: 6px 10px; background: var(--surface-2); border-radius: 6px; }
.checks li { display: flex; gap: 8px; align-items: center; padding: 3px 0; }
.name { font-size: 13px; }
.summary { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
