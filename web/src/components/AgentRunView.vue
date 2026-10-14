<script setup lang="ts">
import { computed, ref } from 'vue'
import type { AgentRun, AgentStep } from '../api'

// The trail under an agent's answer: what it is doing while it works, and
// afterwards every tool call it made, so an answer can always be checked.
const props = defineProps<{ run: AgentRun }>()
const open = ref(false)
const steps = computed(() => props.run.steps ?? [])

const labels: Record<string, string> = {
  search: 'Searched',
  list_issues: 'Listed issues',
  get_issue: 'Read issue',
  get_thread: 'Read thread',
  get_application: 'Read application',
  list_files: 'Listed files',
  read_file: 'Read file',
  list_my_todos: 'Read your to-dos',
  propose_issue: 'Drafted an issue',
  propose_todo: 'Drafted a to-do',
  propose_issue_comment: 'Drafted a comment',
  write_file: 'Wrote file',
  edit_file: 'Edited file',
  run: 'Ran',
}

function describe(s: AgentStep) {
  const i = s.input ?? {}
  const detail =
    i.query ??
    i.command ??
    (i.number ? `${i.app}#${i.number}` : null) ??
    (i.path !== undefined ? (i.app ? `${i.app}/${i.repo}:${i.path}` : i.path || '.') : null) ??
    i.app ??
    i.title ??
    ''
  return `${labels[s.tool] ?? s.tool}${detail ? ' · ' + detail : ''}`
}
</script>

<template>
  <div class="run small">
    <div v-if="run.status === 'running'" class="working">
      <span class="dot" /> {{ steps.length ? describe(steps[steps.length - 1]) + '…' : 'Thinking…' }}
    </div>
    <button v-else-if="steps.length" class="ghost small toggle" @click="open = !open">
      {{ open ? '▾' : '▸' }} {{ steps.length }} {{ steps.length === 1 ? 'step' : 'steps' }}
      <span class="muted">· {{ run.model }}</span>
    </button>
    <ol v-if="open" class="steps">
      <li v-for="(s, i) in steps" :key="i" :class="{ failed: s.error }">
        {{ describe(s) }}
        <span v-if="s.error" class="error">· {{ s.error }}</span>
      </li>
    </ol>
  </div>
</template>

<style scoped>
.run { margin-top: 4px; color: var(--muted); }
.working { display: flex; align-items: center; gap: 8px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1s ease-in-out infinite alternate; }
@keyframes pulse { from { opacity: 0.3; } to { opacity: 1; } }
.toggle { margin-left: -8px; color: var(--muted); }
.steps { margin: 4px 0 0; padding-left: 20px; }
.steps li { overflow-wrap: anywhere; }
.failed { color: var(--muted); }
.error { color: var(--danger); }
</style>
