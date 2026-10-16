<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type ApiToken, type Invite } from '../api'
import { ago } from '../ci'
import RunnerList from '../components/RunnerList.vue'
import { useSession } from '../stores/session'
import { useToasts } from '../stores/toast'

const session = useSession()
const toasts = useToasts()
const tokens = ref<ApiToken[]>([])
const name = ref('')
const days = ref<number | ''>(90)
const secret = ref('')

const invites = ref<Invite[]>([])
const inviteNote = ref('')
const inviteUrl = ref('')

async function load() {
  try {
    tokens.value = await api.tokens()
    if (session.user!.is_admin) invites.value = await api.invites()
  } catch (e) {
    toasts.error(e)
  }
}
onMounted(load)

async function createInvite() {
  try {
    inviteUrl.value = (await api.createInvite(inviteNote.value)).url
    inviteNote.value = ''
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
async function revokeInvite(i: Invite) {
  if (!confirm('Revoke this invite? Its link stops working.')) return
  try {
    await api.deleteInvite(i.id)
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
const inviteState = (i: Invite) =>
  i.used_at ? `used by @${i.used_by ?? 'deleted user'}` : new Date(i.expires_at) < new Date() ? 'expired' : `expires ${new Date(i.expires_at).toLocaleDateString()}`

async function create() {
  try {
    const res = await api.createToken(name.value, days.value === '' ? undefined : Number(days.value))
    secret.value = res.secret
    name.value = ''
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
async function remove(t: ApiToken) {
  if (!confirm(`Revoke the token "${t.name}"? Anything using it stops working.`)) return
  try {
    await api.deleteToken(t.id)
    await load()
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>Settings</h1>
        <div class="small muted">Signed in as <span class="mono">@{{ session.user!.username }}</span></div>
      </div>
    </div>

    <section>
      <h2>Personal access tokens</h2>
      <p class="small muted">
        Tokens act as you in the API, for scripts and external CI. Send one as <span class="mono">Authorization: Bearer &lt;token&gt;</span>.
      </p>
      <div class="card">
        <ul v-if="tokens.length" class="list">
          <li v-for="t in tokens" :key="t.id">
            <strong>{{ t.name }}</strong>
            <span class="small muted">created {{ new Date(t.created_at).toLocaleDateString() }}</span>
            <span class="spacer" />
            <span class="small muted">{{ t.last_used_at ? 'used ' + ago(t.last_used_at) : 'never used' }}</span>
            <span class="small" :class="t.expires_at && new Date(t.expires_at) < new Date() ? 'error' : 'muted'">
              {{ t.expires_at ? 'expires ' + new Date(t.expires_at).toLocaleDateString() : 'no expiry' }}
            </span>
            <button class="ghost small" :aria-label="`Revoke ${t.name}`" @click="remove(t)">✕</button>
          </li>
        </ul>
        <div v-else class="empty">No tokens.</div>
      </div>
      <form class="row add" @submit.prevent="create">
        <input v-model="name" placeholder="Token name, e.g. jenkins" required aria-label="Token name" />
        <input v-model.number="days" type="number" min="1" class="days" placeholder="days" aria-label="Expires in days (empty for no expiry)" />
        <span class="small muted">days</span>
        <button type="submit" :disabled="!name">Create token</button>
      </form>
      <div v-if="secret" class="card pad secret">
        <div class="row">
          <strong class="grow">New token</strong>
          <button class="ghost small" aria-label="Dismiss" @click="secret = ''">✕</button>
        </div>
        <p class="small muted">Shown only once. Copy it now.</p>
        <pre class="cmd">{{ secret }}</pre>
      </div>
    </section>

    <section v-if="session.user!.is_admin">
      <h2>Invites</h2>
      <p class="small muted">An invite link lets one person create an account. It works once and expires after 7 days.</p>
      <div class="card">
        <ul v-if="invites.length" class="list">
          <li v-for="i in invites" :key="i.id">
            <strong>{{ i.note || 'Invite' }}</strong>
            <span class="small muted">created {{ new Date(i.created_at).toLocaleDateString() }}</span>
            <span class="spacer" />
            <span class="small muted">{{ inviteState(i) }}</span>
            <button v-if="!i.used_at" class="ghost small" aria-label="Revoke invite" @click="revokeInvite(i)">✕</button>
          </li>
        </ul>
        <div v-else class="empty">No invites.</div>
      </div>
      <form class="row add" @submit.prevent="createInvite">
        <input v-model="inviteNote" placeholder="Who it is for (optional)" aria-label="Invite note" />
        <button type="submit">Create invite link</button>
      </form>
      <div v-if="inviteUrl" class="card pad secret">
        <div class="row">
          <strong class="grow">New invite link</strong>
          <button class="ghost small" aria-label="Dismiss" @click="inviteUrl = ''">✕</button>
        </div>
        <p class="small muted">Shown only once. Send it to the person you are inviting.</p>
        <pre class="cmd">{{ inviteUrl }}</pre>
      </div>
    </section>

    <section v-if="session.user!.is_admin">
      <h2>Runners</h2>
      <p class="small muted">Runners registered here are shared and pick up jobs from every application. Runners of a single application are listed too.</p>
      <RunnerList :app-key="null" />
    </section>
  </div>
</template>

<style scoped>
section { max-width: 820px; }
.add { margin-top: 8px; }
.add input:first-child { max-width: 300px; }
.days { width: 90px; }
.grow { flex: 1; }
.secret { margin-top: 8px; border-color: var(--accent); }
.secret p { margin: 4px 0; }
.cmd { background: var(--surface-2); padding: 10px 12px; border-radius: 6px; margin: 0; overflow-x: auto; user-select: all; }
</style>
