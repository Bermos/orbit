<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { api, type Message } from '../api'
import { subscribe } from '../realtime'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'
import MessageItem from './MessageItem.vue'

// A review comment on a diff line with its thread, shown under the line.
// The comment is an ordinary message of the merge request's discussion, so
// replies, agent answers, to-dos and issues work as they do in chat.
// earlier marks a comment made on an earlier version whose lines are
// unchanged; outdated, one whose lines changed since.
const props = defineProps<{ root: Message; outdated?: boolean; earlier?: boolean }>()
const emit = defineEmits<{ issue: [m: Message]; links: [m: Message] }>()
const toasts = useToasts()
const todos = useTodos()
const replies = ref<Message[]>([])
const text = ref('')
const busy = ref(false)
const replying = ref(false)

async function loadReplies() {
  if (!props.root.reply_count) {
    replies.value = []
    return
  }
  const all = await api.messages(props.root.channel_id, props.root.id)
  replies.value = all.filter((m) => m.id !== props.root.id)
}
watch(() => [props.root.id, props.root.reply_count], loadReplies, { immediate: true })

// Replies change as they arrive: an agent's answer is updated while it works.
// Private ones come on the user's own topic.
const session = useSession()
function onEvent(type: string, m: Message) {
  if (m.thread_id !== props.root.id || !type.startsWith('message.')) return
  const i = replies.value.findIndex((x) => x.id === m.id)
  if (i >= 0) replies.value[i] = m
  else if (type === 'message.created') replies.value.push(m)
}
const unsubs = [subscribe('channel:' + props.root.channel_id, onEvent)]
if (session.user) unsubs.push(subscribe('user:' + session.user.id, onEvent))
onBeforeUnmount(() => unsubs.forEach((u) => u()))

async function reply() {
  const body = text.value.trim()
  if (!body) return
  busy.value = true
  try {
    await api.postMessage(props.root.channel_id, { body, thread_id: props.root.id })
    text.value = ''
    replying.value = false
    await loadReplies()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
async function addTodo(m: Message) {
  try {
    await todos.add({ source: { type: 'message', id: m.id } })
    toasts.show('Added to your to-dos', { link: '/todos' })
  } catch (e) {
    toasts.error(e)
  }
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) reply()
}
</script>

<template>
  <div class="thread" :class="{ outdated }">
    <div v-if="earlier" class="earlier small muted">Commented on an earlier version</div>
    <MessageItem :message="root" in-thread :hide-code="!outdated" @todo="addTodo(root)" @issue="emit('issue', root)" @links="emit('links', root)" />
    <MessageItem
      v-for="m in replies"
      :key="m.id"
      :message="m"
      in-thread
      hide-code
      class="reply"
      @todo="addTodo(m)"
      @issue="emit('issue', m)"
      @links="emit('links', m)"
    />
    <div class="foot">
      <div v-if="replying" class="stack">
        <textarea v-model="text" rows="2" placeholder="Reply, or ask an agent, for example @assistant" autofocus @keydown="onKey" />
        <div class="row">
          <button class="primary small" :disabled="busy || !text.trim()" @click="reply">Reply</button>
          <button class="ghost small" @click="replying = false">Cancel</button>
        </div>
      </div>
      <div v-else class="row">
        <button class="small" @click="replying = true">Reply</button>
        <RouterLink class="small" :to="{ query: { thread: root.id } }">Open in discussion</RouterLink>
      </div>
    </div>
  </div>
</template>

<style scoped>
.thread { border: 1px solid var(--border); border-radius: 8px; background: var(--surface); margin: 6px 12px 6px 96px; padding: 4px 0; font-family: Inter, system-ui, sans-serif; font-size: 14px; white-space: normal; max-width: 760px; }
.thread.outdated { margin-left: 0; }
.reply { margin-left: 24px; }
.earlier { padding: 2px 16px 0 58px; }
.foot { padding: 4px 16px 8px 58px; }
.foot textarea { min-height: 52px; }
</style>
