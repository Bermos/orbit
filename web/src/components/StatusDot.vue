<script setup lang="ts">
defineProps<{ status: string }>()
</script>

<template>
  <span class="sdot" :class="status" :title="status" />
</template>

<style scoped>
.sdot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; background: var(--muted); flex-shrink: 0; }
.success { background: var(--success); }
.failure, .error { background: var(--danger); }
.pending { background: var(--warning); animation: pulse 1s ease-in-out infinite alternate; }
.running { background: var(--accent); animation: pulse 1s ease-in-out infinite alternate; }
.cancelled, .skipped { background: var(--muted); opacity: 0.6; }
@keyframes pulse { from { opacity: 0.35; } to { opacity: 1; } }
</style>
