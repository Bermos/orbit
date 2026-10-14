<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type Proposal } from '../api'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'
import EntityChip from './EntityChip.vue'

// A write an agent drafted. Only the person who asked can confirm it, and
// confirming runs it with their own permissions.
const props = defineProps<{ proposal: Proposal }>()
const session = useSession()
const toasts = useToasts()
const busy = ref(false)

const mine = computed(() => props.proposal.asker_id === session.user?.id)
const icons = { issue: '◉', todo: '☐', issue_comment: '💬', branch: '⑂' }
const verbs = { issue: 'Create issue', todo: 'Add to-do', issue_comment: 'Post comment', branch: 'Push branch' }

async function act(apply: boolean) {
  busy.value = true
  try {
    if (apply) await api.applyProposal(props.proposal.id)
    else await api.dismissProposal(props.proposal.id)
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="proposal card" :class="proposal.status">
    <div class="head">
      <span class="icon">{{ icons[proposal.kind] }}</span>
      <strong>{{ proposal.summary }}</strong>
    </div>
    <div v-if="proposal.kind !== 'todo' && proposal.input.body" class="body">{{ proposal.input.body }}</div>
    <div class="actions">
      <template v-if="proposal.status === 'pending' || proposal.status === 'applying'">
        <template v-if="mine">
          <button class="primary small" :disabled="busy || proposal.status === 'applying'" @click="act(true)">{{ verbs[proposal.kind] }}</button>
          <button class="ghost small" :disabled="busy" @click="act(false)">Dismiss</button>
        </template>
        <span v-else class="muted small">Waiting for the person who asked to confirm</span>
      </template>
      <template v-else-if="proposal.status === 'applied' && proposal.result">
        <span class="muted small">Done:</span>
        <EntityChip :entity="proposal.result" />
      </template>
      <span v-else class="muted small">Dismissed</span>
    </div>
  </div>
</template>

<style scoped>
.proposal { margin-top: 6px; padding: 10px 12px; max-width: 560px; border-left: 3px solid var(--accent); }
.proposal.dismissed { opacity: 0.6; border-left-color: var(--border); }
.proposal.applied { border-left-color: var(--success); }
.head { display: flex; gap: 8px; align-items: baseline; }
.icon { color: var(--accent); }
.body { margin-top: 4px; white-space: pre-wrap; color: var(--muted); max-height: 160px; overflow-y: auto; }
.actions { display: flex; gap: 6px; align-items: center; margin-top: 8px; }
</style>
