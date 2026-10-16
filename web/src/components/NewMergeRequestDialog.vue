<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, type Branch, type Repo } from '../api'
import Modal from './Modal.vue'

// Opens a merge request. repo and source preselect the repository and the
// branch, for example from the code view of a branch.
const props = defineProps<{ app: string; repo?: string; source?: string }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const repos = ref<Repo[]>([])
const branches = ref<Branch[]>([])
const form = ref({ repo: props.repo ?? '', source: props.source ?? '', target: '', title: '', body: '', draft: false })
const busy = ref(false)
const error = ref('')

onMounted(async () => {
  repos.value = (await api.app(props.app)).repositories
  if (!form.value.repo && repos.value.length) form.value.repo = repos.value[0].name
})
watch(
  () => form.value.repo,
  async (name) => {
    if (!name) return
    const detail = await api.repo(props.app, name)
    branches.value = detail.branches
    form.value.target = detail.repository.default_branch
    if (!branches.value.some((b) => b.name === form.value.source)) {
      form.value.source = branches.value.find((b) => b.name !== form.value.target)?.name ?? ''
    }
  },
  { immediate: true },
)
const ready = computed(() => form.value.repo && form.value.source && form.value.target && form.value.source !== form.value.target)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const res = await api.createMergeRequest(props.app, {
      repo: form.value.repo,
      source_branch: form.value.source,
      target_branch: form.value.target,
      title: form.value.title,
      body: form.value.body,
      draft: form.value.draft,
    })
    emit('close')
    router.push(`/apps/${props.app}/merge-requests/${res.merge_request.number}`)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="New merge request" wide @close="emit('close')">
    <form id="new-mr" @submit.prevent="submit">
      <div v-if="!repo" class="field">
        <label for="mr-repo">Repository</label>
        <select id="mr-repo" v-model="form.repo">
          <option v-for="r in repos" :key="r.id" :value="r.name">{{ r.name }}</option>
        </select>
      </div>
      <div class="row branches">
        <div class="field grow">
          <label for="mr-source">Merge</label>
          <select id="mr-source" v-model="form.source" class="mono">
            <option v-for="b in branches" :key="b.name" :value="b.name">{{ b.name }}</option>
          </select>
        </div>
        <span class="arrow">→</span>
        <div class="field grow">
          <label for="mr-target">into</label>
          <select id="mr-target" v-model="form.target" class="mono">
            <option v-for="b in branches" :key="b.name" :value="b.name">{{ b.name }}</option>
          </select>
        </div>
      </div>
      <div class="field">
        <label for="mr-title">Title</label>
        <input id="mr-title" v-model="form.title" placeholder="Defaults to the commit subject" />
      </div>
      <div class="field">
        <label for="mr-body">Description</label>
        <textarea id="mr-body" v-model="form.body" rows="5" placeholder="What changes and why. &quot;Closes #12&quot; closes that issue when this is merged." />
      </div>
      <label class="check"><input v-model="form.draft" type="checkbox" /> Draft: not ready to merge yet</label>
      <p v-if="error" class="error">{{ error }}</p>
    </form>
    <template #footer>
      <button @click="emit('close')">Cancel</button>
      <button class="primary" form="new-mr" type="submit" :disabled="!ready || busy">Open merge request</button>
    </template>
  </Modal>
</template>

<style scoped>
.branches { align-items: flex-end; }
.grow { flex: 1; min-width: 0; }
.arrow { padding-bottom: 20px; color: var(--muted); }
.check { display: flex; gap: 6px; align-items: center; font-weight: normal; }
.check input { width: auto; margin: 0; }
</style>
