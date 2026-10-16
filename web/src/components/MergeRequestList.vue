<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type MergeRequest } from '../api'
import { ago } from '../ci'
import NewMergeRequestDialog from './NewMergeRequestDialog.vue'
import StatusDot from './StatusDot.vue'

// Merge requests of an application, or of one of its repositories.
const props = defineProps<{ app: string; repo?: string; canCreate?: boolean }>()
const filter = ref('open')
const items = ref<MergeRequest[] | null>(null)
const creating = ref(false)

async function load() {
  items.value = await api.mergeRequests(props.app, filter.value, props.repo)
}
watch(() => [props.app, props.repo, filter.value], load, { immediate: true })
</script>

<template>
  <div>
    <div class="row filter">
      <select v-model="filter" class="narrow" aria-label="Show">
        <option value="open">Open</option>
        <option value="merged">Merged</option>
        <option value="closed">Closed</option>
        <option value="all">All</option>
      </select>
      <span class="spacer" />
      <button v-if="canCreate" class="primary" @click="creating = true">New merge request</button>
    </div>
    <div class="card">
      <div v-if="items === null" class="empty">Loading…</div>
      <ul v-else-if="items.length" class="list">
        <li v-for="m in items" :key="m.id">
          <span class="mr-icon" :class="m.status" :title="m.status">⇄</span>
          <div class="grow">
            <div class="row">
              <RouterLink :to="`/apps/${m.app_key}/merge-requests/${m.number}`" class="title">{{ m.title }}</RouterLink>
              <span v-if="m.draft" class="badge">draft</span>
            </div>
            <div class="small muted">
              <span class="mono">{{ m.app_key }}!{{ m.number }}</span> ·
              <span v-if="!repo" class="mono">{{ m.repo }} · </span>
              <span class="mono">{{ m.source_branch }} → {{ m.target_branch }}</span> ·
              {{ m.author?.display_name ?? 'someone' }}, {{ ago(m.status === 'merged' && m.merged_at ? m.merged_at : m.created_at) }}
            </div>
          </div>
          <StatusDot v-if="m.checks_state" :status="m.checks_state" />
        </li>
      </ul>
      <div v-else class="empty">No {{ filter === 'all' ? '' : filter }} merge requests.</div>
    </div>
    <NewMergeRequestDialog v-if="creating" :app="app" :repo="repo" @close="creating = false" />
  </div>
</template>

<style scoped>
.filter { margin-bottom: 12px; }
.narrow { width: auto; }
.grow { flex: 1; min-width: 0; }
.title { font-weight: 550; }
.mr-icon { font-size: 15px; color: var(--accent); }
.mr-icon.merged { color: var(--success); }
.mr-icon.closed { color: var(--muted); }
</style>
