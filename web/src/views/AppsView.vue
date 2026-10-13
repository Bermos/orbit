<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type App, type Team } from '../api'
import { useToasts } from '../stores/toast'

const router = useRouter()
const toasts = useToasts()
const apps = ref<App[]>([])
const teams = ref<Team[]>([])
const form = ref({ key: '', name: '', description: '', owner_team_id: '' })

onMounted(async () => {
  ;[apps.value, teams.value] = await Promise.all([api.apps(), api.myTeams()])
  if (teams.value.length) form.value.owner_team_id = teams.value[0].id
})

async function create() {
  try {
    await api.createApp(form.value)
    window.dispatchEvent(new Event('orbit:nav-changed'))
    router.push(`/apps/${form.value.key}`)
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>Applications</h1>
        <div class="sub">The stable root of everything: repositories, issues and channels live under an application.</div>
      </div>
    </div>
    <div class="grid2">
      <div class="card">
        <ul v-if="apps.length" class="list">
          <li v-for="a in apps" :key="a.key">
            <div class="grow">
              <RouterLink :to="`/apps/${a.key}`"><strong>{{ a.name }}</strong></RouterLink>
              <span class="mono small muted"> /{{ a.key }}</span>
              <div class="small muted">{{ a.description || 'No description' }}</div>
            </div>
            <span class="badge">{{ a.owner_team.name }}</span>
          </li>
        </ul>
        <div v-else class="empty">No applications yet.</div>
      </div>
      <form class="card pad" @submit.prevent="create">
        <h3 style="margin-top: 0">New application</h3>
        <div class="field">
          <label for="an">Long name</label>
          <input id="an" v-model="form.name" required placeholder="General Ledger" />
        </div>
        <div class="field">
          <label for="ak">Short name</label>
          <input id="ak" v-model="form.key" class="mono" required pattern="[a-z0-9][a-z0-9-]*[a-z0-9]" placeholder="ledger" />
          <div class="hint">Used in URLs and as the ID. It cannot be changed later.</div>
        </div>
        <div class="field">
          <label for="ad">Description</label>
          <textarea id="ad" v-model="form.description" rows="2" />
        </div>
        <div class="field">
          <label for="ao">Owning team</label>
          <select id="ao" v-model="form.owner_team_id" required>
            <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
          <div v-if="!teams.length" class="hint">Join or <RouterLink to="/teams">create a team</RouterLink> first.</div>
        </div>
        <button class="primary" type="submit" :disabled="!form.key || !form.name || !form.owner_team_id">Create application</button>
      </form>
    </div>
  </div>
</template>

<style scoped>
.grow { flex: 1; min-width: 0; }
</style>
