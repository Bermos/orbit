<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Channel } from '../api'
import ChatPanel from '../components/ChatPanel.vue'

const props = defineProps<{ id: string }>()
const router = useRouter()
const route = useRoute()
const channel = ref<Channel | null>(null)

watch(
  () => props.id,
  async (id) => {
    const res = await api.channel(id)
    // Issue discussions live on the issue page.
    if (res.issue_url) {
      router.replace({ path: res.issue_url, query: route.query })
      return
    }
    channel.value = res.channel
  },
  { immediate: true },
)
</script>

<template>
  <div v-if="channel" class="channel">
    <header class="bar">
      <h1><span class="muted">#</span>{{ channel.name }}</h1>
      <RouterLink v-if="channel.app_key" :to="`/apps/${channel.app_key}`" class="badge accent">{{ channel.app_key }}</RouterLink>
    </header>
    <ChatPanel :channel-id="channel.id" :app-key="channel.app_key" />
  </div>
</template>

<style scoped>
.channel { display: flex; flex-direction: column; height: 100%; }
.bar { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border); background: var(--surface); }
.bar h1 { font-size: 17px; margin: 0; }
.channel > :last-child { flex: 1; min-height: 0; }
</style>
