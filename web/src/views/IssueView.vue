<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type Issue, type IssueStatus, type LinkView, type User } from '../api'
import ChatPanel from '../components/ChatPanel.vue'
import LinkList from '../components/LinkList.vue'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'

const props = defineProps<{ app: string; number: number }>()
const toasts = useToasts()
const todos = useTodos()
const issue = ref<Issue | null>(null)
const links = ref<LinkView[]>([])
const users = ref<User[]>([])
const editing = ref(false)
const draft = ref({ title: '', body: '' })

async function load() {
  const res = await api.issue(props.app, props.number)
  issue.value = res.issue
  links.value = res.links
  if (!users.value.length) users.value = await api.users()
}
watch(() => [props.app, props.number], load, { immediate: true })

async function update(patch: Parameters<typeof api.updateIssue>[2]) {
  try {
    const res = await api.updateIssue(props.app, props.number, patch)
    issue.value = res.issue
    links.value = res.links
  } catch (e) {
    toasts.error(e)
    await load()
  }
}
function startEdit() {
  draft.value = { title: issue.value!.title, body: issue.value!.body }
  editing.value = true
}
async function saveEdit() {
  await update(draft.value)
  editing.value = false
}
async function addTodo() {
  try {
    await todos.add({ source: { type: 'issue', id: issue.value!.id } })
    toasts.show('Added to your to-dos', { link: '/todos' })
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
const statuses: { value: IssueStatus; label: string }[] = [
  { value: 'open', label: 'Open' },
  { value: 'in_progress', label: 'In progress' },
  { value: 'done', label: 'Done' },
  { value: 'closed', label: 'Closed' },
]
</script>

<template>
  <div v-if="issue" class="issue">
    <div class="top">
      <div class="crumbs small muted">
        <RouterLink :to="`/apps/${app}`">{{ app }}</RouterLink> / <RouterLink :to="`/apps/${app}?tab=issues`">issues</RouterLink> /
        #{{ issue.number }}
      </div>
      <div v-if="!editing" class="head">
        <h1>{{ issue.title }} <span class="muted">#{{ issue.number }}</span></h1>
        <div class="row">
          <button class="small" @click="addTodo">+ To-do</button>
          <button class="small" @click="startEdit">Edit</button>
        </div>
      </div>
      <form v-else class="stack edit" @submit.prevent="saveEdit">
        <input v-model="draft.title" required aria-label="Title" />
        <textarea v-model="draft.body" rows="5" aria-label="Description" />
        <div class="row">
          <button class="primary" type="submit">Save</button>
          <button type="button" @click="editing = false">Cancel</button>
        </div>
      </form>
    </div>
    <div class="cols">
      <div class="left">
        <div v-if="issue.body && !editing" class="card pad body">{{ issue.body }}</div>
        <h3 class="disc">Discussion</h3>
        <div class="discussion card">
          <ChatPanel :channel-id="issue.channel_id" :app-key="app" empty-text="No comments yet." />
        </div>
      </div>
      <aside class="side">
        <div class="field">
          <label for="st">Status</label>
          <select id="st" :value="issue.status" @change="update({ status: ($event.target as HTMLSelectElement).value as IssueStatus })">
            <option v-for="s in statuses" :key="s.value" :value="s.value">{{ s.label }}</option>
          </select>
        </div>
        <div class="field">
          <label for="as">Assignee</label>
          <select id="as" :value="issue.assignee?.id ?? ''" @change="update({ assignee_id: ($event.target as HTMLSelectElement).value })">
            <option value="">Unassigned</option>
            <option v-for="u in users" :key="u.id" :value="u.id">{{ u.display_name }}</option>
          </select>
        </div>
        <div class="field">
          <label>Reported by</label>
          <div>{{ issue.author?.display_name ?? 'Unknown' }}</div>
          <div class="small muted">{{ new Date(issue.created_at).toLocaleString() }}</div>
        </div>
        <div class="field">
          <label>Linked</label>
          <LinkList :links="links" />
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.issue { padding: 24px 32px; display: flex; flex-direction: column; height: 100%; }
.head { display: flex; align-items: flex-start; gap: 16px; }
.head h1 { flex: 1; }
.edit { max-width: 760px; }
.cols { display: grid; grid-template-columns: 1fr 260px; gap: 24px; flex: 1; min-height: 0; margin-top: 12px; }
.left { display: flex; flex-direction: column; min-height: 0; }
.body { white-space: pre-wrap; margin-bottom: 8px; max-height: 30vh; overflow-y: auto; }
.disc { margin: 8px 0; }
.discussion { flex: 1; min-height: 420px; overflow: hidden; }
.side { overflow-y: auto; }
@media (max-width: 900px) { .cols { grid-template-columns: 1fr; } .issue { height: auto; } }
</style>
