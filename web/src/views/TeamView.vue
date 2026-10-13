<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Issue, type SpaceApp, type Team, type TeamDetail, type User } from '../api'
import Modal from '../components/Modal.vue'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'

const props = defineProps<{ id: string }>()
const session = useSession()
const toasts = useToasts()
const detail = ref<TeamDetail | null>(null)
const space = ref<{ applications: SpaceApp[]; open_issues: Issue[] } | null>(null)
const users = ref<User[]>([])
const allTeams = ref<Team[]>([])
const editing = ref(false)
const form = ref({ name: '', slug: '', parent: '' })
const newMember = ref('')

async function load() {
  ;[detail.value, space.value] = await Promise.all([api.team(props.id), api.teamSpace(props.id)])
}
watch(() => props.id, load, { immediate: true })

const canManage = computed(
  () =>
    session.user?.is_admin ||
    detail.value?.members.some((m) => m.id === session.user?.id && m.role === 'lead'),
)

// Group the space by application: one app can be held through several teams.
const apps = computed(() => {
  const m = new Map<string, { app: SpaceApp; via: { id: string; name: string; relation: string }[] }>()
  for (const a of space.value?.applications ?? []) {
    const e = m.get(a.key) ?? { app: a, via: [] }
    e.via.push({ id: a.via_id, name: a.via_name, relation: a.relation })
    m.set(a.key, e)
  }
  return [...m.values()]
})

async function openEdit() {
  const t = detail.value!.team
  form.value = { name: t.name, slug: t.slug, parent: t.parent_id ?? '' }
  allTeams.value = await api.teams()
  editing.value = true
}
async function save() {
  try {
    detail.value = await api.updateTeam(props.id, { name: form.value.name, slug: form.value.slug, parent_id: form.value.parent })
    editing.value = false
    toasts.show('Team updated. Application paths are unchanged.')
  } catch (e) {
    toasts.error(e)
  }
}
async function loadUsers() {
  if (!users.value.length) users.value = await api.users()
}
const addable = computed(() => users.value.filter((u) => !detail.value?.members.some((m) => m.id === u.id)))
async function addMember() {
  if (!newMember.value) return
  try {
    detail.value = await api.putTeamMember(props.id, newMember.value, 'member')
    newMember.value = ''
  } catch (e) {
    toasts.error(e)
  }
}
async function setRole(userId: string, role: string) {
  try {
    detail.value = await api.putTeamMember(props.id, userId, role)
  } catch (e) {
    toasts.error(e)
  }
}
async function remove(userId: string) {
  try {
    detail.value = await api.removeTeamMember(props.id, userId)
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div v-if="detail" class="page">
    <div class="crumbs small muted">
      <RouterLink to="/teams">Teams</RouterLink>
      <template v-for="a in detail.ancestors" :key="a.id">
        / <RouterLink :to="`/teams/${a.id}`">{{ a.name }}</RouterLink>
      </template>
    </div>
    <div class="page-header">
      <div>
        <h1>{{ detail.team.name }}</h1>
        <div class="sub mono small">{{ detail.team.slug }}</div>
      </div>
      <span class="spacer" />
      <button v-if="canManage" @click="openEdit">Rename or move</button>
    </div>

    <div class="grid2">
      <section>
        <h2 style="margin-top: 0">Team space</h2>
        <p class="muted small">
          Everything this team and its sub-teams own or are responsible for. It is a view: the applications keep their own
          paths.
        </p>
        <div class="card">
          <ul v-if="apps.length" class="list">
            <li v-for="{ app, via } in apps" :key="app.key" class="app">
              <div class="grow">
                <RouterLink :to="`/apps/${app.key}`"><strong>{{ app.name }}</strong></RouterLink>
                <span class="mono small muted"> /{{ app.key }}</span>
                <div class="row wrap small">
                  <span v-for="v in via" :key="v.id + v.relation" class="badge" :class="{ accent: v.relation === 'owner' }">
                    {{ v.relation === 'owner' ? 'owned' : v.relation }} via {{ v.name }}
                  </span>
                </div>
              </div>
            </li>
          </ul>
          <div v-else class="empty">No applications mapped to this team yet.</div>
        </div>

        <h2>Open issues across the space</h2>
        <div class="card">
          <ul v-if="space?.open_issues.length" class="list">
            <li v-for="i in space.open_issues" :key="i.id">
              <span :class="`status-${i.status}`">●</span>
              <RouterLink :to="`/apps/${i.app_key}/issues/${i.number}`">{{ i.title }}</RouterLink>
              <span class="mono small muted">{{ i.app_key }}#{{ i.number }}</span>
              <span class="spacer" />
              <span v-if="i.assignee" class="small muted">{{ i.assignee.display_name }}</span>
            </li>
          </ul>
          <div v-else class="empty">No open issues.</div>
        </div>
      </section>

      <aside>
        <h2 style="margin-top: 0">Members</h2>
        <div class="card">
          <ul class="list">
            <li v-for="m in detail.members" :key="m.id">
              <span class="grow">{{ m.display_name }} <span class="muted small">@{{ m.username }}</span></span>
              <select v-if="canManage" :value="m.role" class="role" @change="setRole(m.id, ($event.target as HTMLSelectElement).value)">
                <option value="member">member</option>
                <option value="lead">lead</option>
              </select>
              <span v-else class="badge">{{ m.role }}</span>
              <button v-if="canManage" class="ghost small" :aria-label="`Remove ${m.display_name}`" @click="remove(m.id)">✕</button>
            </li>
          </ul>
          <form v-if="canManage" class="row add" @submit.prevent="addMember">
            <select v-model="newMember" @focus="loadUsers">
              <option value="">Add a member…</option>
              <option v-for="u in addable" :key="u.id" :value="u.id">{{ u.display_name }} (@{{ u.username }})</option>
            </select>
            <button type="submit" :disabled="!newMember">Add</button>
          </form>
        </div>
        <h2>Sub-teams</h2>
        <div class="card">
          <ul v-if="detail.children.length" class="list">
            <li v-for="c in detail.children" :key="c.id">
              <RouterLink :to="`/teams/${c.id}`">{{ c.name }}</RouterLink>
              <span class="spacer" />
              <span class="muted small">{{ c.member_count }} members</span>
            </li>
          </ul>
          <div v-else class="empty small">None</div>
        </div>
      </aside>
    </div>

    <Modal v-if="editing" title="Rename or move team" @close="editing = false">
      <form id="edit-team" @submit.prevent="save">
        <div class="field">
          <label for="en">Name</label>
          <input id="en" v-model="form.name" required />
        </div>
        <div class="field">
          <label for="es">Slug</label>
          <input id="es" v-model="form.slug" class="mono" required />
        </div>
        <div class="field">
          <label for="ep">Parent team</label>
          <select id="ep" v-model="form.parent">
            <option value="">None (top level)</option>
            <option v-for="t in allTeams.filter((t) => t.id !== id)" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
          <div class="hint">Applications owned by this team keep their paths and URLs.</div>
        </div>
      </form>
      <template #footer>
        <button @click="editing = false">Cancel</button>
        <button class="primary" form="edit-team" type="submit">Save</button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.crumbs { margin-bottom: 6px; }
.grow { flex: 1; min-width: 0; }
.app .row { margin-top: 4px; }
.role { width: auto; padding: 2px 6px; font-size: 12px; }
.add { padding: 10px 14px; border-top: 1px solid var(--border); }
</style>
