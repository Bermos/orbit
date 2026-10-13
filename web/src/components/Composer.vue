<script setup lang="ts">
import { ref } from 'vue'
const props = defineProps<{ placeholder?: string; busy?: boolean }>()
const emit = defineEmits<{ send: [body: string] }>()
const text = ref('')

function send() {
  const body = text.value.trim()
  if (!body || props.busy) return
  emit('send', body)
  text.value = ''
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    send()
  }
}
</script>

<template>
  <div class="composer">
    <textarea v-model="text" :placeholder="placeholder ?? 'Write a message'" rows="2" @keydown="onKey" />
    <button class="primary" :disabled="!text.trim() || busy" @click="send">Send</button>
  </div>
</template>

<style scoped>
.composer { display: flex; gap: 8px; align-items: flex-end; padding: 12px 16px; border-top: 1px solid var(--border); background: var(--surface); }
textarea { min-height: 40px; max-height: 200px; }
</style>
