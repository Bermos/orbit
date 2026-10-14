<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type AppDetail, type Webhook, type WebhookDelivery, type WebhookEvent } from '../api'
import { ago } from '../ci'
import { useToasts } from '../stores/toast'
import RunnerList from './RunnerList.vue'

const props = defineProps<{ detail: AppDetail }>()
const toasts = useToasts()
const app = () => props.detail.application.key
const hooks = ref<Webhook[]>([])
const form = ref({ url: '', repo: '', events: ['push', 'check', 'pipeline'] as WebhookEvent[] })
const secret = ref('')
const openHook = ref<string | null>(null)
const deliveries = ref<WebhookDelivery[]>([])
const allEvents: WebhookEvent[] = ['push', 'check', 'pipeline']
const origin = location.origin

async function load() {
  try {
    hooks.value = await api.webhooks(app())
  } catch (e) {
    toasts.error(e)
  }
}
watch(() => props.detail.application.key, load, { immediate: true })

async function create() {
  try {
    const res = await api.createWebhook(app(), { url: form.value.url, repo: form.value.repo || null, events: form.value.events })
    secret.value = res.secret
    form.value.url = ''
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
async function remove(h: Webhook) {
  if (!confirm(`Delete the webhook to ${h.url}?`)) return
  try {
    await api.deleteWebhook(app(), h.id)
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
async function toggleDeliveries(h: Webhook) {
  if (openHook.value === h.id) {
    openHook.value = null
    return
  }
  openHook.value = h.id
  deliveries.value = []
  try {
    deliveries.value = await api.webhookDeliveries(app(), h.id)
  } catch (e) {
    toasts.error(e)
  }
}
const ok = (code: number | null) => code !== null && code >= 200 && code < 300
</script>

<template>
  <section>
    <h2>Webhooks</h2>
    <p class="small muted">Orbit sends a signed POST to these URLs when code is pushed, a check changes or a pipeline finishes.</p>
    <div class="card">
      <ul v-if="hooks.length" class="list">
        <li v-for="h in hooks" :key="h.id" class="hook">
          <div class="line">
            <div class="text">
              <div class="mono url">{{ h.url }}</div>
              <div class="small muted">
                {{ h.repo ?? 'all repositories' }} · {{ h.events.join(', ') }}
                <template v-if="h.last_delivery_at">
                  ·
                  <span :class="ok(h.last_status) ? 'ok' : 'bad'">{{ h.last_status ?? h.last_error ?? 'failed' }}</span>
                  {{ ago(h.last_delivery_at) }}
                </template>
                <template v-else> · no deliveries yet</template>
              </div>
            </div>
            <button class="ghost small" @click="toggleDeliveries(h)">{{ openHook === h.id ? 'Hide' : 'Deliveries' }}</button>
            <button class="ghost small" :aria-label="`Delete webhook ${h.url}`" @click="remove(h)">✕</button>
          </div>
          <ul v-if="openHook === h.id" class="deliveries small">
            <li v-if="!deliveries.length" class="muted">No deliveries.</li>
            <li v-for="d in deliveries" :key="d.id">
              <span :class="ok(d.status_code) ? 'ok' : 'bad'" class="mono">{{ d.status_code || 'ERR' }}</span>
              <span>{{ d.event }}</span>
              <span class="muted">{{ d.duration_ms }} ms</span>
              <span v-if="d.error" class="bad err">{{ d.error }}</span>
              <span class="spacer" />
              <span class="muted" :title="new Date(d.created_at).toLocaleString()">{{ ago(d.created_at) }}</span>
            </li>
          </ul>
        </li>
      </ul>
      <div v-else class="empty">No webhooks.</div>
    </div>
    <form class="card pad stack add" @submit.prevent="create">
      <div class="row">
        <input v-model="form.url" type="url" class="mono" placeholder="https://example.com/hook" required aria-label="Webhook URL" />
        <select v-model="form.repo" class="narrow" aria-label="Repository">
          <option value="">All repositories</option>
          <option v-for="r in detail.repositories" :key="r.id" :value="r.name">{{ r.name }}</option>
        </select>
      </div>
      <div class="row">
        <label v-for="e in allEvents" :key="e" class="check">
          <input v-model="form.events" type="checkbox" :value="e" />
          {{ e }}
        </label>
        <span class="spacer" />
        <button type="submit" :disabled="!form.url || !form.events.length">Add webhook</button>
      </div>
    </form>
    <div v-if="secret" class="card pad secret">
      <div class="row">
        <strong class="grow">Signing secret</strong>
        <button class="ghost small" aria-label="Dismiss" @click="secret = ''">✕</button>
      </div>
      <p class="small muted">Shown only once. Copy it now and use it to verify the <span class="mono">X-Orbit-Signature</span> header (HMAC-SHA256 of the body) on each delivery.</p>
      <pre class="cmd">{{ secret }}</pre>
    </div>

    <h2>Runners</h2>
    <p class="small muted">Runners execute this application's pipeline jobs. Shared runners are managed by administrators.</p>
    <RunnerList :app-key="detail.application.key" />

    <h2>Checks API</h2>
    <div class="card pad small">
      External CI can report results on a commit with a
      <RouterLink to="/settings/tokens">personal access token</RouterLink>:
      <pre class="cmd">curl -X POST -H "Authorization: Bearer $ORBIT_TOKEN" -H "Content-Type: application/json" \
  {{ origin }}/api/v1/apps/{{ detail.application.key }}/repos/&lt;repo&gt;/commits/&lt;sha&gt;/checks \
  -d '{"name": "build", "status": "success", "summary": "All tests passed", "details_url": "https://…"}'</pre>
      <span class="muted">Status is one of pending, running, success, failure, error, cancelled or skipped. Posting the same name again updates the check.</span>
    </div>
  </section>
</template>

<style scoped>
section { max-width: 820px; }
.hook { flex-direction: column; align-items: stretch; }
.line { display: flex; gap: 8px; align-items: center; }
.text { flex: 1; min-width: 0; }
.url { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ok { color: var(--success); }
.bad { color: var(--danger); }
.deliveries { list-style: none; margin: 6px 0 0; padding: 6px 10px; background: var(--surface-2); border-radius: 6px; }
.deliveries li { display: flex; gap: 10px; padding: 2px 0; }
.err { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.add { margin-top: 8px; }
.narrow { width: auto; }
.check { display: inline-flex; align-items: center; gap: 4px; font-weight: normal; margin: 0 8px 0 0; }
.check input { width: auto; }
.grow { flex: 1; }
.secret { margin-top: 8px; border-color: var(--accent); }
.secret p { margin: 4px 0; }
.cmd { background: var(--surface-2); padding: 10px 12px; border-radius: 6px; margin: 8px 0; overflow-x: auto; }
.secret .cmd { user-select: all; margin: 0; }
</style>
