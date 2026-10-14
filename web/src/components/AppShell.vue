<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type App, type Channel } from '../api'
import { startRealtime } from '../realtime'
import { useSession } from '../stores/session'
import { useTodos } from '../stores/todos'

const session = useSession()
const todos = useTodos()
const router = useRouter()
const apps = ref<App[]>([])
const channels = ref<Channel[]>([])

async function refresh() {
  ;[apps.value, channels.value] = await Promise.all([api.apps(), api.channels()])
}
onMounted(async () => {
  startRealtime()
  await Promise.all([refresh(), todos.load(session.user!.id)])
})
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
