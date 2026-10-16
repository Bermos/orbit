<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Channel } from '../api'
import ChatPanel from '../components/ChatPanel.vue'
import { useAgents } from '../stores/agents'
import { chatTitle, useChats } from '../stores/chats'
import { useToasts } from '../stores/toast'

const props = defineProps<{ id: string }>()
const router = useRouter()
const route = useRoute()
const agents = useAgents()
const chats = useChats()
const toasts = useToasts()
const channel = ref<Channel | null>(null)

watch(
  () => props.id,
  async (id) => {
    const res = await api.channel(id)
    // Issue and merge request discussions live on their pages.
    const page = res.issue_url ?? res.merge_request_url
    if (page) {
      router.replace({ path: page, query: route.query })
      return
    }
    channel.value = res.channel
  },
  { immediate: true },
)

// A private chat's title fills in from its first message.
watch(
  () => chats.items.find((c) => c.id === channel.value?.id)?.name,
  (name) => {
    if (channel.value && name) channel.value = { ...channel.value, name }
  },
)

const direct = computed(() => channel.value?.kind === 'direct')
const agent = computed(() => agents.items.find((a) => a.name === channel.value?.agent))

async function remove() {
  if (!channel.value || !confirm('Delete this chat? Its messages are deleted too.')) return
  try {
    await chats.remove(channel.value.id)
    router.push('/')
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div v-if="channel" class="channel">
    <header v-if="direct" class="bar">
      <h1>{{ chatTitle(channel) }}</h1>
      <span class="badge" :title="agent?.description">@{{ channel.agent }}</span>
      <span class="muted small">🔒 Only you see this chat</span>
      <span class="spacer" />
      <button class="ghost small" @click="remove">Delete chat</button>
    </header>
    <header v-else class="bar">
      <h1><span class="muted">#</span>{{ channel.name }}</h1>
      <RouterLink v-if="channel.app_key" :to="`/apps/${channel.app_key}`" class="badge accent">{{ channel.app_key }}</RouterLink>
    </header>
    <ChatPanel
      :channel-id="channel.id"
      :app-key="channel.app_key"
      :agent="direct ? channel.agent : undefined"
      :empty-text="direct ? `Ask @${channel.agent} anything. It answers with what you and it may both read, and drafts changes for you to confirm.` : undefined"
    />
  </div>
</template>

<style scoped>
.channel { display: flex; flex-direction: column; height: 100%; }
.bar { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border); background: var(--surface); }
.bar h1 { font-size: 17px; margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.channel > :last-child { flex: 1; min-height: 0; }
</style>
