<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type App, type Message } from '../api'
import { useToasts } from '../stores/toast'
import Modal from './Modal.vue'

const props = defineProps<{ source: Message; defaultApp?: string | null }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()
const toasts = useToasts()

const apps = ref<App[]>([])
const app = ref(props.defaultApp ?? '')
const firstLine = props.source.body.split('\n')[0] ?? ''
const title = ref(firstLine.length > 80 ? firstLine.slice(0, 79) + '…' : firstLine)
const body = ref(props.source.body ? props.source.body.split('\n').map((l) => '> ' + l).join('\n') + '\n\n' : '')
const busy = ref(false)
const error = ref('')

onMounted(async () => {
  apps.value = await api.apps()
  if (!app.value && apps.value.length) app.value = apps.value[0].key
})

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const res = await api.createIssue(app.value, {
      title: title.value,
      body: body.value,
      source: { type: 'message', id: props.source.id },
    })
    toasts.show(`Created ${res.issue.app_key}#${res.issue.number}`, { link: `/apps/${res.issue.app_key}/issues/${res.issue.number}` })
    emit('close')
    router.push(`/apps/${res.issue.app_key}/issues/${res.issue.number}`)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Create issue from conversation" wide @close="emit('close')">
    <form id="create-issue" @submit.prevent="submit">
      <div class="field">
        <label for="ci-app">Application</label>
        <select id="ci-app" v-model="app" required>
          <option v-for="a in apps" :key="a.key" :value="a.key">{{ a.name }} ({{ a.key }})</option>
        </select>
      </div>
      <div class="field">
        <label for="ci-title">Title</label>
        <input id="ci-title" v-model="title" required autofocus />
      </div>
      <div class="field">
        <label for="ci-body">Description</label>
        <textarea id="ci-body" v-model="body" rows="6" />
        <div class="hint">The issue links back to this message, and the conversation gets a note pointing at the issue.</div>
      </div>
      <div v-if="error" class="error">{{ error }}</div>
    </form>
    <template #footer>
      <button @click="emit('close')">Cancel</button>
      <button class="primary" form="create-issue" type="submit" :disabled="busy || !app || !title">Create issue</button>
    </template>
  </Modal>
</template>
