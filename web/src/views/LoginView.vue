<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type AuthConfig } from '../api'
import { useSession } from '../stores/session'

const session = useSession()
const router = useRouter()
const route = useRoute()
// Links from an admin (?invite=) and from the startup log (?setup=) open the
// sign-up form with the code filled in.
const invite = (route.query.invite as string) || ''
const setupToken = ref((route.query.setup as string) || '')
const config = ref<AuthConfig | null>(null)
const mode = ref<'login' | 'register'>(invite || setupToken.value ? 'register' : 'login')
const username = ref('')
const displayName = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

const canRegister = computed(
  () => !!invite || !!config.value?.setup_required || config.value?.registration === 'open',
)

onMounted(async () => {
  try {
    config.value = await api.authConfig()
    if (config.value.setup_required) mode.value = 'register'
  } catch {
    // The sign-in form works without it.
  }
})

async function submit() {
  busy.value = true
  error.value = ''
  try {
    if (mode.value === 'login') await session.login(username.value, password.value)
    else
      await session.register({
        username: username.value,
        display_name: displayName.value,
        password: password.value,
        invite: invite || undefined,
        setup_token: config.value?.setup_token_required ? setupToken.value : undefined,
      })
    router.replace((route.query.next as string) || '/')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="wrap">
    <form class="card pad box" @submit.prevent="submit">
      <div class="brand">
        <svg viewBox="0 0 32 32" width="34" height="34" aria-hidden="true">
          <circle cx="16" cy="16" r="5" fill="currentColor" />
          <ellipse cx="16" cy="16" rx="13" ry="6" fill="none" stroke="currentColor" stroke-width="2" transform="rotate(-25 16 16)" />
        </svg>
        <h1>Orbit</h1>
      </div>
      <p class="muted">Code, chat, issues and planning in one place.</p>
      <p v-if="mode === 'register' && config?.setup_required" class="small">
        Welcome! Create the first account. It becomes the instance admin.
      </p>
      <div v-if="mode === 'register' && config?.setup_token_required" class="field">
        <label for="s">Setup token</label>
        <input id="s" v-model="setupToken" class="mono" autocomplete="off" required />
        <div class="hint">Orbit logs a setup link with this token when it starts without any accounts.</div>
      </div>
      <div class="field">
        <label for="u">Username</label>
        <input id="u" v-model="username" autocomplete="username" required autofocus />
      </div>
      <div v-if="mode === 'register'" class="field">
        <label for="d">Display name</label>
        <input id="d" v-model="displayName" autocomplete="name" />
      </div>
      <div class="field">
        <label for="p">Password</label>
        <input id="p" v-model="password" type="password" :autocomplete="mode === 'login' ? 'current-password' : 'new-password'" required />
        <div v-if="mode === 'register'" class="hint">At least 8 characters.</div>
      </div>
      <div v-if="error" class="error">{{ error }}</div>
      <button class="primary full" type="submit" :disabled="busy">{{ mode === 'login' ? 'Sign in' : 'Create account' }}</button>
      <p class="switch muted small">
        <template v-if="mode === 'login' && canRegister">No account yet? <a href="#" @click.prevent="mode = 'register'">Create one</a></template>
        <template v-else-if="mode === 'login'">No account yet? Ask an admin for an invite.</template>
        <template v-else>Already have an account? <a href="#" @click.prevent="mode = 'login'">Sign in</a></template>
      </p>
    </form>
  </div>
</template>

<style scoped>
.wrap { min-height: 100%; display: flex; align-items: center; justify-content: center; padding: 16px; }
.box { width: 360px; }
.brand { display: flex; align-items: center; gap: 10px; color: var(--accent); }
.brand h1 { color: var(--text); margin: 0; }
.full { width: 100%; justify-content: center; margin-top: 4px; }
.switch { text-align: center; margin-bottom: 0; }
</style>
