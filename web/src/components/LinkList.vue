<script setup lang="ts">
import type { LinkView } from '../api'
import EntityChip from './EntityChip.vue'
defineProps<{ links: LinkView[] }>()

// Read each edge from the point of view of the entity being shown.
function label(l: LinkView): string {
  const out: Record<string, string> = {
    created_from: 'Created from',
    discusses: 'Discusses',
    mentions: 'Mentions',
    relates_to: 'Related to',
  }
  const inbound: Record<string, string> = {
    created_from: 'Led to',
    discusses: 'Discussed in',
    mentions: 'Mentioned in',
    relates_to: 'Related to',
  }
  return (l.direction === 'out' ? out : inbound)[l.kind] ?? l.kind
}
</script>

<template>
  <ul v-if="links.length" class="links">
    <li v-for="l in links" :key="l.id + l.direction">
      <span class="kind">{{ label(l) }}</span>
      <EntityChip :entity="l.entity" />
    </li>
  </ul>
  <div v-else class="muted small">Nothing linked yet.</div>
</template>

<style scoped>
.links { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; }
.links li { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.kind { font-size: 11.5px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
</style>
