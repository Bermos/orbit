<script setup lang="ts">
import type { Entity } from '../api'
defineProps<{ entity: Entity }>()

const icons: Record<string, string> = { message: '💬', issue: '◉', todo: '☐', code: '⟨⟩', repo: '⎇', app: '▣' }
</script>

<template>
  <RouterLink :to="entity.url || '#'" class="chip" :class="{ missing: entity.missing }">
    <span class="icon">{{ icons[entity.type] ?? '•' }}</span>
    <span class="title">{{ entity.title }}</span>
    <span v-if="entity.subtitle" class="sub">{{ entity.subtitle }}</span>
  </RouterLink>
</template>

<style scoped>
.chip {
  display: inline-flex; align-items: center; gap: 6px; max-width: 100%;
  padding: 3px 10px; border-radius: 6px; border: 1px solid var(--border); background: var(--surface-2);
  color: var(--text); font-size: 13px;
}
.chip:hover { border-color: var(--accent); text-decoration: none; }
.chip.missing { opacity: 0.6; pointer-events: none; }
.icon { color: var(--accent); font-size: 12px; }
.title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 4em; }
.sub { color: var(--muted); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex-shrink: 2; }
</style>
