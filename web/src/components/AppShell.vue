<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type App, type Channel } from '../api'
import { startRealtime } from '../realtime'
import { useAgents } from '../stores/agents'
import { chatTitle, useChats } from '../stores/chats'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'

const session = useSession()
const todos = useTodos()
const chats = useChats()
const agents = useAgents()
const toasts = useToasts()
const router = useRouter()
const picking = ref(false)
const apps = ref<App[]>([])
const channels = ref<Channel[]>([])

async function refresh() {
  ;[apps.value, channels.value] = await Promise.all([api.apps(), api.channels()])
}
onMounted(async () => {
  startRealtime()
  await Promise.all([refresh(), todos.load(session.user!.id), chats.load(session.user!.id), agents.load()])
})

// A new private chat: with the only agent straight away, otherwise pick one.
async function newChat(agent?: string) {
  if (!agent && agents.items.length !== 1) {
    picking.value = !picking.value
    return
  }
  picking.value = false
  try {
    const c = await chats.start(agent ?? agents.items[0].name)
    router.push(`/channels/${c.id}`)
  } catch (e) {
    toasts.error(e)
  }
}
window.addEventListener('orbit:nav-changed', refresh)

async function logout() {
  await session.logout()
  router.push('/login')
}
</script>

<template>
  <div class="shell">
    <nav class="sidebar">
      <RouterLink to="/" class="brand">
        <svg viewBox="0 0 32 32" width="22" height="22" aria-hidden="true">
          <circle cx="16" cy="16" r="5" fill="currentColor" />
          <ellipse cx="16" cy="16" rx="13" ry="6" fill="none" stroke="currentColor" stroke-width="2" transform="rotate(-25 16 16)" />
        </svg>
        Orbit
      </RouterLink>
      <div class="section">
        <RouterLink to="/" class="item" exact-active-class="active">Home</RouterLink>
        <RouterLink to="/todos" class="item" active-class="active">
          To-dos <span v-if="todos.open.length" class="count">{{ todos.open.length }}</span>
        </RouterLink>
        <RouterLink to="/teams" class="item" active-class="active">Teams</RouterLink>
        <RouterLink to="/apps" class="item" exact-active-class="active">Applications</RouterLink>
      </div>
      <div v-if="agents.items.length" class="section">
        <div class="heading row">
          Chats with agents
          <button class="add" title="New chat with an agent" aria-label="New chat with an agent" @click="newChat()">+</button>
        </div>
        <div v-if="picking" class="picker">
          <button v-for="a in agents.items" :key="a.name" class="item pick" :title="a.description" @click="newChat(a.name)">
            <span class="bot">@</span>{{ a.name }}
            <span class="app-tag">{{ a.display_name }}</span>
          </button>
        </div>
        <RouterLink v-for="c in chats.items.slice(0, 15)" :key="c.id" :to="`/channels/${c.id}`" class="item" active-class="active" :title="chatTitle(c)">
          <span class="chat-title">{{ chatTitle(c) }}</span>
          <span class="app-tag">@{{ c.agent }}</span>
        </RouterLink>
        <div v-if="!chats.items.length && !picking" class="item muted-item">Start a private chat with +</div>
      </div>
      <div class="section">
        <div class="heading">Applications</div>
        <RouterLink v-for="a in apps" :key="a.key" :to="`/apps/${a.key}`" class="item" active-class="active" :title="a.name">
          <span class="mono key">{{ a.key }}</span>
        </RouterLink>
        <div v-if="!apps.length" class="item muted-item">None yet</div>
      </div>
      <div class="section">
        <div class="heading">Channels</div>
        <RouterLink v-for="c in channels" :key="c.id" :to="`/channels/${c.id}`" class="item" active-class="active">
          <span class="hash">#</span>{{ c.name }}
          <span v-if="c.app_key" class="app-tag">{{ c.app_key }}</span>
        </RouterLink>
      </div>
      <div class="spacer" />
      <div class="me">
        <span class="avatar">{{ session.user!.display_name.slice(0, 1).toUpperCase() }}</span>
        <span class="name">{{ session.user!.display_name }}</span>
        <RouterLink to="/settings/tokens" class="settings" title="Settings" aria-label="Settings">⚙</RouterLink>
        <button class="ghost small" @click="logout">Sign out</button>
      </div>
    </nav>
    <main class="main"><slot /></main>
  </div>
</template>

<style scoped>
.shell { display: flex; height: 100%; }
.sidebar {
  width: 240px;
  flex-shrink: 0;
  background: var(--sidebar);
  color: var(--sidebar-text);
  display: flex;
  flex-direction: column;
  padding: 14px 10px;
  overflow-y: auto;
}
.brand { display: flex; align-items: center; gap: 8px; color: white; font-weight: 700; font-size: 17px; padding: 4px 8px 14px; }
.brand:hover { text-decoration: none; }
.brand svg { color: var(--accent); }
.section { margin-bottom: 14px; }
.heading { font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; opacity: 0.6; padding: 4px 8px; }
.item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 6px;
  color: var(--sidebar-text);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.item:hover { background: var(--sidebar-active); text-decoration: none; }
.item.active { background: var(--sidebar-active); color: white; }
.muted-item { opacity: 0.5; }
.heading.row { display: flex; align-items: center; }
.add {
  margin-left: auto; padding: 0 7px; line-height: 18px; border-radius: 6px; font-size: 15px;
  background: transparent; color: var(--sidebar-text); border: none; opacity: 1;
}
.add:hover { background: var(--sidebar-active); }
.picker { margin: 2px 0 6px; padding: 4px; border-radius: 8px; background: #ffffff0d; }
.pick { width: 100%; background: transparent; border: none; text-align: left; font: inherit; cursor: pointer; }
.bot { opacity: 0.5; }
.chat-title { overflow: hidden; text-overflow: ellipsis; }
.hash { opacity: 0.5; }
.app-tag { margin-left: auto; font-size: 11px; opacity: 0.5; }
.count { margin-left: auto; background: var(--accent); color: white; border-radius: 999px; padding: 0 7px; font-size: 11px; }
.key { font-size: 12.5px; }
.me { display: flex; align-items: center; gap: 8px; padding: 8px; border-top: 1px solid #ffffff14; }
.me .name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.me .settings { color: var(--sidebar-text); padding: 2px 6px; border-radius: 6px; }
.me .settings:hover, .me .settings.router-link-active { background: var(--sidebar-active); text-decoration: none; }
.me button { color: var(--sidebar-text); }
.me button:hover { background: var(--sidebar-active); }
.avatar {
  width: 26px; height: 26px; border-radius: 50%; background: var(--accent); color: white;
  display: inline-flex; align-items: center; justify-content: center; font-weight: 650; font-size: 12px;
}
.main { flex: 1; overflow-y: auto; min-width: 0; }
</style>
