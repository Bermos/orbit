<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from './components/AppShell.vue'
import Toasts from './components/Toasts.vue'
import { useSession } from './stores/session'

const session = useSession()
const route = useRoute()
const router = useRouter()

onMounted(() => {
  window.addEventListener('orbit:unauthorized', () => {
    // Only a session that ended while in use; signed-out visitors are
    // handled by the router guard, which keeps the URL they opened.
    if (!session.user) return
    session.user = null
    router.push({ path: '/login', query: { next: route.fullPath } })
  })
})
watch(
  () => session.user,
  (u) => {
    if (!u && !route.meta.public && session.loaded) router.push('/login')
  },
)
</script>

<template>
  <RouterView v-if="route.meta.public" />
  <AppShell v-else-if="session.user">
    <RouterView :key="route.path" />
  </AppShell>
  <Toasts />
</template>
