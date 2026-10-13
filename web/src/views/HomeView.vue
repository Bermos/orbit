<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type App, type Team } from '../api'
import EntityChip from '../components/EntityChip.vue'
import { useSession } from '../stores/session'
import { useTodos } from '../stores/todos'

const session = useSession()
const todos = useTodos()
const teams = ref<Team[]>([])
const apps = ref<App[]>([])
onMounted(async () => {
  ;[teams.value, apps.value] = await Promise.all([api.myTeams(), api.apps()])
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>Hi {{ session.user!.display_name }}</h1>
        <div class="sub">Here is what is waiting for you.</div>
      </div>
    </div>
    <div class="grid2">
      <section>
        <h2>Open to-dos</h2>
        <div class="card">
          <ul v-if="todos.open.length" class="list">
            <li v-for="t in todos.open.slice(0, 8)" :key="t.id">
              <input type="checkbox" class="check" :checked="t.done" @change="todos.toggle(t)" />
              <div class="stack grow">
                <span>{{ t.title }}</span>
                <EntityChip v-if="t.source" :entity="t.source" />
              </div>
            </li>
          </ul>
          <div v-else class="empty">Nothing to do. Add to-dos from any message, issue or code selection.</div>
        </div>
        <RouterLink to="/todos" class="small">All to-dos</RouterLink>
      </section>
      <section>
        <h2>Your team spaces</h2>
        <div class="card">
          <ul v-if="teams.length" class="list">
            <li v-for="t in teams" :key="t.id">
              <RouterLink :to="`/teams/${t.id}`">{{ t.name }}</RouterLink>
              <span class="muted small">{{ t.member_count }} members</span>
            </li>
          </ul>
          <div v-else class="empty">You are not in a team yet. <RouterLink to="/teams">Create or join one</RouterLink></div>
        </div>
        <h2>Applications</h2>
        <div class="card">
          <ul v-if="apps.length" class="list">
            <li v-for="a in apps" :key="a.key">
              <RouterLink :to="`/apps/${a.key}`">{{ a.name }}</RouterLink>
              <span class="mono small muted">{{ a.key }}</span>
            </li>
          </ul>
          <div v-else class="empty"><RouterLink to="/apps">Create the first application</RouterLink></div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.grow { flex: 1; min-width: 0; align-items: flex-start; gap: 4px; }
.check { width: auto; }
</style>
