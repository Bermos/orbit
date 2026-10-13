<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type Channel, type CodeInput, type Issue } from '../api'
import { useToasts } from '../stores/toast'
import Modal from './Modal.vue'

// "Discuss these lines": posts the selected range, pinned to its commit, into
// a channel, an existing issue or a new issue, then jumps to that message.
const props = defineProps<{ code: CodeInput; lines: string[] }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()
const toasts = useToasts()

const target = ref<'channel' | 'issue' | 'new-issue'>('channel')
const channels = ref<Channel[]>([])
const issues = ref<Issue[]>([])
const channelId = ref('')
const issueNumber = ref<number | null>(null)
const newTitle = ref('')
const body = ref('')
const busy = ref(false)

onMounted(async () => {
  const [appChannels, all, active] = await Promise.all([api.channels(props.code.app), api.channels(), api.issues(props.code.app)])
  channels.value = [...appChannels, ...all.filter((c) => !appChannels.some((a) => a.id === c.id))]
  issues.value = active
  channelId.value = channels.value[0]?.id ?? ''
  issueNumber.value = active[0]?.number ?? null
})

const label = computed(() => `${props.code.path}:${props.code.start}${props.code.end > props.code.start ? '-' + props.code.end : ''}`)

async function submit() {
  busy.value = true
  try {
    let channel = ''
    let base = ''
    if (target.value === 'channel') {
      channel = channelId.value
      base = `/channels/${channel}`
    } else {
      let issue: Issue
      if (target.value === 'new-issue') {
        issue = (await api.createIssue(props.code.app, { title: newTitle.value, body: body.value })).issue
      } else {
        issue = issues.value.find((i) => i.number === issueNumber.value)!
      }
      channel = issue.channel_id
      base = `/apps/${issue.app_key}/issues/${issue.number}`
    }
    const m = await api.postMessage(channel, { body: body.value, code: props.code })
    emit('close')
    router.push({ path: base, query: { message: m.id } })
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :title="`Discuss ${label}`" wide @close="emit('close')">
    <form id="discuss" @submit.prevent="submit">
      <pre class="preview"><code><span v-for="(l, i) in lines" :key="i" class="line"><span class="ln">{{ code.start + i }}</span>{{ l || ' ' }}
</span></code></pre>
      <div class="field targets">
        <label><input v-model="target" type="radio" value="channel" /> In a channel</label>
        <label><input v-model="target" type="radio" value="issue" :disabled="!issues.length" /> On an existing issue</label>
        <label><input v-model="target" type="radio" value="new-issue" /> In a new issue</label>
      </div>
      <div v-if="target === 'channel'" class="field">
        <select v-model="channelId" aria-label="Channel" required>
          <option v-for="c in channels" :key="c.id" :value="c.id">#{{ c.name }}{{ c.app_key ? ` (${c.app_key})` : '' }}</option>
        </select>
      </div>
      <div v-else-if="target === 'issue'" class="field">
        <select v-model="issueNumber" aria-label="Issue" required>
          <option v-for="i in issues" :key="i.id" :value="i.number">#{{ i.number }} {{ i.title }}</option>
        </select>
      </div>
      <div v-else class="field">
        <input v-model="newTitle" placeholder="Issue title" aria-label="Issue title" required />
      </div>
      <div class="field">
        <textarea v-model="body" rows="3" placeholder="What do you want to say about these lines?" aria-label="Message" />
      </div>
    </form>
    <template #footer>
      <button @click="emit('close')">Cancel</button>
      <button class="primary" form="discuss" type="submit" :disabled="busy">Post</button>
    </template>
  </Modal>
</template>

<style scoped>
.preview { margin: 0 0 12px; padding: 6px 0; border: 1px solid var(--border); border-radius: 6px; max-height: 200px; overflow: auto; background: var(--surface-2); }
.line { display: block; white-space: pre; padding-right: 12px; }
.ln { display: inline-block; width: 44px; padding-right: 12px; text-align: right; color: var(--muted); user-select: none; }
.targets { display: flex; gap: 16px; flex-wrap: wrap; }
.targets label { font-weight: 400; display: flex; gap: 6px; align-items: center; }
.targets input { width: auto; }
</style>
