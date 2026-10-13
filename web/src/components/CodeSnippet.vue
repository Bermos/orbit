<script setup lang="ts">
import type { CodeRange } from '../api'
defineProps<{ code: CodeRange; lines: string[]; url: string }>()
</script>

<template>
  <div class="snippet">
    <RouterLink :to="url" class="head">
      <span class="mono">{{ code.repo }}/{{ code.path }}</span>
      <span class="muted">lines {{ code.start }}–{{ code.end }} @ {{ code.commit.slice(0, 8) }}</span>
    </RouterLink>
    <pre><code><span v-for="(l, i) in lines" :key="i" class="line"><span class="ln">{{ code.start + i }}</span>{{ l || ' ' }}
</span></code></pre>
  </div>
</template>

<style scoped>
.snippet { border: 1px solid var(--border); border-radius: 6px; overflow: hidden; margin-top: 6px; max-width: 760px; }
.head { display: flex; gap: 10px; padding: 5px 10px; background: var(--surface-2); font-size: 12px; color: var(--text); border-bottom: 1px solid var(--border); }
.head:hover { text-decoration: none; background: var(--accent-soft); }
pre { margin: 0; padding: 6px 0; overflow-x: auto; background: var(--surface); }
.line { display: block; padding-right: 12px; white-space: pre; }
.ln { display: inline-block; width: 44px; padding-right: 12px; text-align: right; color: var(--muted); user-select: none; }
</style>
