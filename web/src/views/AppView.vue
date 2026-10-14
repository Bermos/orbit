<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, roleRank, type AppDetail, type Issue, type Team } from '../api'
import CiSettings from '../components/CiSettings.vue'
import Modal from '../components/Modal.vue'
import { useToasts } from '../stores/toast'

const props = defineProps<{ app: string }>()
const route = useRoute()
const router = useRouter()
const toasts = useToasts()
const detail = ref<AppDetail | null>(null)
const issues = ref<Issue[]>([])
const issueFilter = ref('active')
const teams = ref<Team[]>([])
const tab = computed(() => (route.query.tab as string) || 'overview')

const showRepo = ref(false)
const repoForm = ref({ name: '', description: '' })
const showChannel = ref(false)
const channelName = ref('')
const showIssue = ref(false)
const issueForm = ref({ title: '', body: '' })
const mapTeam = ref('')
const mapRole = ref('developer')

const canMaintain = computed(() => detail.value && roleRank[detail.value.my_role] >= roleRank.maintainer)
const canDevelop = computed(() => detail.value && roleRank[detail.value.my_role] >= roleRank.developer)

async function load() {
  detail.value = await api.app(props.app)
}
async function loadIssues() {
  issues.value = await api.issues(props.app, issueFilter.value)
}
watch(() => props.app, load, { immediate: true })
watch([() => props.app, issueFilter, tab], () => tab.value === 'issues' && loadIssues(), { immediate: true })

async function createRepo() {
  try {
    await api.createRepo(props.app, repoForm.value.name, repoForm.value.description)
    router.push(`/apps/${props.app}/repos/${repoForm.value.name}`)
  } catch (e) {
    toasts.error(e)
  }
}
async function createChannel() {
  try {
    const c = await api.createChannel(channelName.value, props.app)
    window.dispatchEvent(new Event('orbit:nav-changed'))
    router.push(`/channels/${c.id}`)
  } catch (e) {
    toasts.error(e)
  }
}
async function createIssue() {
  try {
    const res = await api.createIssue(props.app, issueForm.value)
    router.push(`/apps/${props.app}/issues/${res.issue.number}`)
  } catch (e) {
    toasts.error(e)
  }
}
async function loadTeams() {
  if (!teams.value.length) teams.value = await api.teams()
}
async function addMapping() {
  try {
    detail.value = await api.putAppTeam(props.app, mapTeam.value, mapRole.value)
    mapTeam.value = ''
  } catch (e) {
    toasts.error(e)
  }
}
async function removeMapping(team: string) {
  try {
    detail.value = await api.removeAppTeam(props.app, team)
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div v-if="detail" class="page">
    <div class="page-header">
      <div>
        <h1>{{ detail.application.name }}</h1>
        <div class="row small">
          <span class="mono muted">/{{ detail.application.key }}</span>
          <span class="muted">·</span>
          <span class="muted">owned by</span>
          <RouterLink :to="`/teams/${detail.application.owner_team.id}`">{{ detail.application.owner_team.name }}</RouterLink>
          <span class="badge">you: {{ detail.my_role }}</span>
        </div>
        <p v-if="detail.application.description" class="muted">{{ detail.application.description }}</p>
      </div>
    </div>
    <nav class="tabs">
      <RouterLink :to="{ query: {} }" :class="{ active: tab === 'overview' }">Overview</RouterLink>
      <RouterLink :to="{ query: { tab: 'issues' } }" :class="{ active: tab === 'issues' }">Issues</RouterLink>
      <RouterLink v-if="canMaintain" :to="{ query: { tab: 'ci' } }" :class="{ active: tab === 'ci' }">CI</RouterLink>
    </nav>

    <div v-if="tab === 'overview'" class="grid2">
      <section>
        <div class="row">
          <h2 class="grow">Repositories</h2>
          <button v-if="canMaintain" class="small" @click="showRepo = true">New repository</button>
        </div>
        <div class="card">
          <ul v-if="detail.repositories.length" class="list">
            <li v-for="r in detail.repositories" :key="r.id">
              <RouterLink :to="`/apps/${app}/repos/${r.name}`" class="mono">{{ r.name }}</RouterLink>
              <span class="small muted">{{ r.description }}</span>
            </li>
          </ul>
          <div v-else class="empty">No repositories yet.</div>
        </div>
        <div class="row">
          <h2 class="grow">Channels</h2>
          <button v-if="canDevelop" class="small" @click="showChannel = true">New channel</button>
        </div>
        <div class="card">
          <ul class="list">
            <li v-for="c in detail.channels" :key="c.id">
              <RouterLink :to="`/channels/${c.id}`"><span class="muted">#</span>{{ c.name }}</RouterLink>
            </li>
          </ul>
        </div>
      </section>
      <aside>
        <h2>Ownership</h2>
        <div class="card pad">
          <div class="small muted">Owning team</div>
          <RouterLink :to="`/teams/${detail.application.owner_team.id}`">{{ detail.application.owner_team.name }}</RouterLink>
          <div class="small muted" style="margin-top: 12px">Responsible teams</div>
          <ul v-if="detail.responsible_teams.length" class="plain">
            <li v-for="t in detail.responsible_teams" :key="t.id" class="row">
              <RouterLink :to="`/teams/${t.id}`">{{ t.name }}</RouterLink>
              <span class="badge">{{ t.role }}</span>
              <span class="spacer" />
              <button v-if="canMaintain" class="ghost small" :aria-label="`Remove ${t.name}`" @click="removeMapping(t.id)">✕</button>
            </li>
          </ul>
          <div v-else class="small muted">None</div>
          <form v-if="canMaintain" class="stack map" @submit.prevent="addMapping">
            <select v-model="mapTeam" @focus="loadTeams">
              <option value="">Add a responsible team…</option>
              <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
            </select>
            <div class="row">
              <select v-model="mapRole">
                <option value="developer">developer</option>
                <option value="maintainer">maintainer</option>
              </select>
              <button type="submit" :disabled="!mapTeam">Add</button>
            </div>
          </form>
          <p class="small muted hint">Changing teams changes access, never this application's path.</p>
        </div>
      </aside>
    </div>

    <CiSettings v-else-if="tab === 'ci'" :detail="detail" />

    <div v-else>
      <div class="row filter">
        <select v-model="issueFilter" class="narrow">
          <option value="active">Open and in progress</option>
          <option value="finished">Done and closed</option>
          <option value="all">All</option>
        </select>
        <span class="spacer" />
        <button class="primary" @click="showIssue = true">New issue</button>
      </div>
      <div class="card">
        <ul v-if="issues.length" class="list">
          <li v-for="i in issues" :key="i.id">
            <span :class="`status-${i.status}`" :title="i.status">●</span>
            <RouterLink :to="`/apps/${app}/issues/${i.number}`">{{ i.title }}</RouterLink>
            <span class="mono small muted">#{{ i.number }}</span>
            <span class="spacer" />
            <span v-if="i.assignee" class="small muted">{{ i.assignee.display_name }}</span>
          </li>
        </ul>
        <div v-else class="empty">No issues here.</div>
      </div>
    </div>

    <Modal v-if="showRepo" title="New repository" @close="showRepo = false">
      <form id="new-repo" @submit.prevent="createRepo">
        <div class="field">
          <label for="rn">Name</label>
          <input id="rn" v-model="repoForm.name" class="mono" required autofocus pattern="[a-z0-9][a-z0-9._-]*" />
        </div>
        <div class="field">
          <label for="rd">Description</label>
          <input id="rd" v-model="repoForm.description" />
        </div>
      </form>
      <template #footer>
        <button @click="showRepo = false">Cancel</button>
        <button class="primary" form="new-repo" type="submit">Create</button>
      </template>
    </Modal>
    <Modal v-if="showChannel" title="New channel" @close="showChannel = false">
      <form id="new-channel" @submit.prevent="createChannel">
        <div class="field">
          <label for="cn">Name</label>
          <input id="cn" v-model="channelName" required autofocus />
        </div>
      </form>
      <template #footer>
        <button @click="showChannel = false">Cancel</button>
        <button class="primary" form="new-channel" type="submit">Create</button>
      </template>
    </Modal>
    <Modal v-if="showIssue" title="New issue" wide @close="showIssue = false">
      <form id="new-issue" @submit.prevent="createIssue">
        <div class="field">
          <label for="it">Title</label>
          <input id="it" v-model="issueForm.title" required autofocus />
        </div>
        <div class="field">
          <label for="ib">Description</label>
          <textarea id="ib" v-model="issueForm.body" rows="6" />
        </div>
      </form>
      <template #footer>
        <button @click="showIssue = false">Cancel</button>
        <button class="primary" form="new-issue" type="submit">Create issue</button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.grow { flex: 1; }
.plain { list-style: none; padding: 0; margin: 4px 0 0; display: flex; flex-direction: column; gap: 4px; }
.map { margin-top: 12px; }
.hint { margin-bottom: 0; }
.filter { margin-bottom: 12px; }
.narrow { width: auto; }
</style>
