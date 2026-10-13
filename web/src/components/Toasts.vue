<script setup lang="ts">
import { useToasts } from '../stores/toast'
const toasts = useToasts()
</script>

<template>
  <div class="toasts">
    <div v-for="t in toasts.items" :key="t.id" class="toast" :class="t.kind">
      {{ t.text }}
      <RouterLink v-if="t.link" :to="t.link">Open</RouterLink>
    </div>
  </div>
</template>

<style scoped>
.toasts { position: fixed; bottom: 20px; right: 20px; display: flex; flex-direction: column; gap: 8px; z-index: 100; }
.toast {
  background: var(--sidebar); color: white; padding: 10px 14px; border-radius: 8px;
  box-shadow: 0 6px 24px #0003; display: flex; gap: 12px; align-items: center; max-width: 420px;
}
.toast.error { background: var(--danger); }
.toast a { color: #c9c2ff; font-weight: 600; }
</style>
