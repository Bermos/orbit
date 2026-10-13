<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type Team } from '../api'
import { useToasts } from '../stores/toast'

const router = useRouter()
const toasts = useToasts()
const teams = ref<Team[]>([])
const name = ref('')
const slug = ref('')
const parent = ref('')
const slugTouched = ref(false)

onMounted(async () => (teams.value = await api.teams()))

// Flatten the org tree in display order with depth for indentation.
const tree = computed(() => {
  const byParent = new Map<string | null, Team[]>()
  for (const t of teams.value) {
    const k = t.parent_id
    byParent.set(k, [...(byParent.get(k) ?? []), t])
  }
  const out: { team: Team; depth: number }[] = []
  const walk = (p: string | null, depth: number) => {
    for (const t of byParent.get(p) ?? []) {
      out.push({ team: t, depth })
      walk(t.id, depth + 1)
    }
  }
  walk(null, 0)
  return out
})

function onName() {
  if (!slugTouched.value) slug.value = name.value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

async function create() {
  try {
    const res = await api.createTeam(slug.value, name.value, parent.value || null)
    router.push(`/teams/${res.team.id}`)
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>Teams</h1>
        <div class="sub">The org tree. Rename, merge or move teams freely: application paths never change.</div>
      </div>
    </div>
    <div class="grid2">
      <div class="card">
        <ul v-if="tree.length" class="list">
          <li v-for="{ team, depth } in tree" :key="team.id" :style="{ paddingLeft: 14 + depth * 22 + 'px' }">
            <span v-if="depth" class="muted">└</span>
            <RouterLink :to="`/teams/${team.id}`">{{ team.name }}</RouterLink>
            <span class="mono small muted">{{ team.slug }}</span>
            <span class="spacer" />
            <span class="muted small">{{ team.member_count }} members</span>
          </li>
        </ul>
        <div v-else class="empty">No teams yet.</div>
      </div>
      <form class="card pad" @submit.prevent="create">
        <h3 style="margin-top: 0">New team</h3>
        <div class="field">
          <label for="tn">Name</label>
          <input id="tn" v-model="name" required @input="onName" />
        </div>
        <div class="field">
          <label for="ts">Slug</label>
          <input id="ts" v-model="slug" class="mono" required pattern="[a-z0-9][a-z0-9-]*" @input="slugTouched = true" />
        </div>
        <div class="field">
          <label for="tp">Parent team</label>
          <select id="tp" v-model="parent">
            <option value="">None (top level)</option>
            <option v-for="{ team, depth } in tree" :key="team.id" :value="team.id">{{ '— '.repeat(depth) }}{{ team.name }}</option>
          </select>
        </div>
        <button class="primary" type="submit" :disabled="!name || !slug">Create team</button>
      </form>
    </div>
  </div>
</template>
