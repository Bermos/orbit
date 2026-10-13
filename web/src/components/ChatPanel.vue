<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Message } from '../api'
import { subscribe } from '../realtime'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'
import Composer from './Composer.vue'
import CreateIssueDialog from './CreateIssueDialog.vue'
import LinksDialog from './LinksDialog.vue'
import MessageItem from './MessageItem.vue'

// A channel's conversation with threads. Deep links (?message=, ?thread=)
// open the right thread and highlight the exact message, which is how a
// to-do or a link jumps straight back to its context.
const props = defineProps<{ channelId: string; appKey?: string | null; emptyText?: string }>()

const route = useRoute()
const router = useRouter()
const toasts = useToasts()
const todos = useTodos()

const messages = ref<Message[]>([])
const thread = ref<Message[] | null>(null)
const threadId = ref<string | null>(null)
const loading = ref(true)
const sending = ref(false)
const issueSource = ref<Message | null>(null)
const linksFor = ref<Message | null>(null)
const list = ref<HTMLElement | null>(null)

let unsub: (() => void) | null = null

function replace(arr: Message[], m: Message) {
  const i = arr.findIndex((x) => x.id === m.id)
  if (i >= 0) arr[i] = m
  return i >= 0
}

function onEvent(type: string, m: Message) {
  if (type === 'message.created') {
    if (!m.thread_id) {
      if (!replace(messages.value, m)) {
        messages.value.push(m)
        scrollToBottom()
      }
    } else if (m.thread_id === threadId.value && thread.value && !replace(thread.value, m)) {
      thread.value.push(m)
    }
  } else if (type === 'message.updated') {
    replace(messages.value, m)
    if (thread.value) replace(thread.value, m)
  }
}

async function scrollToBottom() {
  await nextTick()
  if (list.value) list.value.scrollTop = list.value.scrollHeight
}

async function highlight(id: string) {
  await nextTick()
  const el = document.getElementById(`msg-${id}`)
  if (!el) return
  el.scrollIntoView({ block: 'center' })
  el.classList.remove('flash')
  void el.offsetWidth
  el.classList.add('flash')
}

async function openThread(id: string | null, updateUrl = true) {
  threadId.value = id
  thread.value = id ? await api.messages(props.channelId, id) : null
  if (updateUrl) {
    const query = { ...route.query }
    if (id) query.thread = id
    else delete query.thread
    delete query.message
    router.replace({ query })
  }
}

async function load() {
  loading.value = true
  unsub?.()
  unsub = subscribe('channel:' + props.channelId, onEvent)
  messages.value = await api.messages(props.channelId)
  loading.value = false
  await applyDeepLink()
}

async function applyDeepLink() {
  const t = (route.query.thread as string) || null
  const m = (route.query.message as string) || null
  if (t) await openThread(t, false)
  else if (threadId.value) await openThread(null, false)
  if (m) highlight(m)
  else if (!t) scrollToBottom()
}

watch(() => props.channelId, load, { immediate: true })
watch(() => [route.query.message, route.query.thread], () => !loading.value && applyDeepLink())
onBeforeUnmount(() => unsub?.())

async function send(body: string, inThread: boolean) {
  sending.value = true
  try {
    const m = await api.postMessage(props.channelId, { body, thread_id: inThread ? threadId.value : null })
    onEvent('message.created', m)
  } catch (e) {
    toasts.error(e)
  } finally {
    sending.value = false
  }
}

async function addTodo(m: Message) {
  try {
    await todos.add({ source: { type: 'message', id: m.id } })
    toasts.show('Added to your to-dos', { link: '/todos' })
    const fresh = await api.messages(props.channelId, m.thread_id ?? m.id)
    for (const x of fresh) onEvent('message.updated', x)
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="chat">
    <div class="main-col">
      <div ref="list" class="messages">
        <div v-if="loading" class="empty">Loading…</div>
        <div v-else-if="!messages.length" class="empty">{{ emptyText ?? 'No messages yet. Start the conversation.' }}</div>
        <MessageItem
          v-for="m in messages"
          :key="m.id"
          :message="m"
          @reply="openThread(m.id)"
          @todo="addTodo(m)"
          @issue="issueSource = m"
          @links="linksFor = m"
        />
      </div>
      <Composer :busy="sending" @send="send($event, false)" />
    </div>
    <aside v-if="threadId" class="thread">
      <header>
        <strong>Thread</strong>
        <span class="spacer" />
        <button class="ghost small" aria-label="Close thread" @click="openThread(null)">✕</button>
      </header>
      <div class="messages">
        <template v-if="thread">
          <MessageItem
            v-for="(m, i) in thread"
            :key="m.id"
            :message="m"
            in-thread
            :class="{ root: i === 0 }"
            @todo="addTodo(m)"
            @issue="issueSource = m"
            @links="linksFor = m"
          />
        </template>
      </div>
      <Composer placeholder="Reply in thread" :busy="sending" @send="send($event, true)" />
    </aside>
    <CreateIssueDialog v-if="issueSource" :source="issueSource" :default-app="appKey" @close="issueSource = null" />
    <LinksDialog v-if="linksFor" :target="{ type: 'message', id: linksFor.id }" @close="linksFor = null" />
  </div>
</template>

<style scoped>
.chat { display: flex; height: 100%; min-height: 0; }
.main-col { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.messages { flex: 1; overflow-y: auto; padding: 12px 0; }
.thread { width: 420px; border-left: 1px solid var(--border); display: flex; flex-direction: column; background: var(--surface); }
.thread header { display: flex; align-items: center; padding: 10px 16px; border-bottom: 1px solid var(--border); }
.thread .root { border-bottom: 1px solid var(--border); margin-bottom: 8px; padding-bottom: 12px; }
@media (max-width: 1000px) { .thread { width: 100%; position: absolute; inset: 0; z-index: 5; } .chat { position: relative; } }
</style>
