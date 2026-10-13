<script setup lang="ts">
defineProps<{ title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <div class="backdrop" @mousedown.self="emit('close')" @keydown.esc="emit('close')">
    <div class="modal card" :class="{ wide }" role="dialog" :aria-label="title">
      <header>
        <h3>{{ title }}</h3>
        <button class="ghost small" aria-label="Close" @click="emit('close')">✕</button>
      </header>
      <div class="body"><slot /></div>
      <footer v-if="$slots.footer"><slot name="footer" /></footer>
    </div>
  </div>
</template>

<style scoped>
.backdrop { position: fixed; inset: 0; background: #0006; display: flex; align-items: flex-start; justify-content: center; padding-top: 10vh; z-index: 50; }
.modal { width: 480px; max-width: calc(100vw - 32px); max-height: 80vh; display: flex; flex-direction: column; box-shadow: 0 20px 60px #0004; }
.modal.wide { width: 680px; }
header { display: flex; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--border); }
header h3 { margin: 0; flex: 1; }
.body { padding: 16px; overflow-y: auto; }
footer { padding: 12px 16px; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 8px; }
</style>
