<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type Runner } from '../api'
import { ago } from '../ci'
import { useToasts } from '../stores/toast'

// Runners for one application, or the shared ones when appKey is null.
const props = defineProps<{ appKey: string | null }>()
const toasts = useToasts()
const runners = ref<Runner[]>([])
const name = ref('')
const token = ref('')
const origin = location.origin

async function load() {
  try {
    runners.value = await api.runners(props.appKey ?? undefined)
  } catch (e) {
    toasts.error(e)
  }
}
watch(() => props.appKey, load, { immediate: true })

async function create() {
  try {
    const res = await api.createRunner(name.value, props.appKey)
    token.value = res.token
    name.value = ''
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
async function remove(r: Runner) {
  if (!confirm(`Remove runner ${r.name}? It will stop picking up jobs.`)) return
  try {
    await api.deleteRunner(r.id)
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="card">
    <ul v-if="runners.length" class="list">
      <li v-for="r in runners" :key="r.id">
        <span class="mono">{{ r.name }}</span>
        <RouterLink v-if="!appKey && r.app_key" :to="`/apps/${r.app_key}?tab=ci`" class="badge">{{ r.app_key }}</RouterLink>
        <span v-else-if="!appKey" class="badge accent">shared</span>
        <span v-if="r.executor" class="badge">{{ r.executor }}</span>
        <span v-if="r.version" class="small muted">{{ r.version }}</span>
        <span class="spacer" />
        <span class="small muted">{{ r.last_seen_at ? 'seen ' + ago(r.last_seen_at) : 'never connected' }}</span>
        <button class="ghost small" :aria-label="`Remove ${r.name}`" @click="remove(r)">✕</button>
      </li>
    </ul>
    <div v-else class="empty">No runners registered.</div>
  </div>
  <form class="row add" @submit.prevent="create">
    <input v-model="name" class="mono" placeholder="Runner name" required aria-label="Runner name" />
    <button type="submit" :disabled="!name">Register runner</button>
  </form>
  <div v-if="token" class="card pad secret">
    <div class="row">
      <strong class="grow">Runner token</strong>
      <button class="ghost small" aria-label="Dismiss" @click="token = ''">✕</button>
    </div>
    <p class="small muted">Shown only once. Start the runner with:</p>
    <pre class="cmd">ORBIT_URL={{ origin }} ORBIT_RUNNER_TOKEN={{ token }} orbit-runner</pre>
  </div>
</template>

<style scoped>
.add { margin-top: 8px; }
.add input { max-width: 280px; }
.grow { flex: 1; }
.secret { margin-top: 8px; border-color: var(--accent); }
.secret p { margin: 4px 0; }
.cmd { background: var(--surface-2); padding: 10px 12px; border-radius: 6px; margin: 0; overflow-x: auto; user-select: all; }
</style>
