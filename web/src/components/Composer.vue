<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useAgents } from '../stores/agents'

const props = defineProps<{ placeholder?: string; busy?: boolean; forcePrivate?: boolean }>()
const emit = defineEmits<{ send: [body: string, isPrivate: boolean] }>()
const agents = useAgents()
const text = ref('')
const onlyMe = ref(false)

onMounted(() => agents.load())

// "Answer only me" appears as soon as the message mentions an agent: the
// question and the answer then stay visible to the author alone.
const mentioned = computed(() => agents.mentioned(text.value))
const hint = computed(() => {
  if (props.placeholder) return props.placeholder
  const a = agents.items[0]
  return a ? `Write a message, or ask @${a.name}` : 'Write a message'
})

function send() {
  const body = text.value.trim()
  if (!body || props.busy) return
  emit('send', body, props.forcePrivate || (onlyMe.value && mentioned.value.length > 0))
  text.value = ''
  onlyMe.value = false
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
    <div class="input">
      <textarea v-model="text" :placeholder="hint" rows="2" @keydown="onKey" />
      <div v-if="forcePrivate" class="options muted small">🔒 This thread is private: only you see it.</div>
      <label v-else-if="mentioned.length" class="options small" :title="`Only you will see this question and the answer. @${mentioned[0].name} may then use your personal data, such as your to-dos.`">
        <input v-model="onlyMe" type="checkbox" />
        Answer only me
        <span class="muted">· otherwise @{{ mentioned.map((a) => a.name).join(', @') }} only uses what everyone here can read</span>
      </label>
    </div>
    <button class="primary" :disabled="!text.trim() || busy" @click="send">Send</button>
  </div>
</template>

<style scoped>
.composer { display: flex; gap: 8px; align-items: flex-end; padding: 12px 16px; border-top: 1px solid var(--border); background: var(--surface); }
.input { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
textarea { min-height: 40px; max-height: 200px; }
.options { display: flex; align-items: center; gap: 6px; }
.options input { width: auto; margin: 0; }
</style>
