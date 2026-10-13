<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSession } from '../stores/session'

const session = useSession()
const router = useRouter()
const route = useRoute()
const mode = ref<'login' | 'register'>('login')
const username = ref('')
const displayName = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    if (mode.value === 'login') await session.login(username.value, password.value)
    else await session.register(username.value, displayName.value, password.value)
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
        <div v-if="mode === 'register'" class="hint">At least 8 characters. The first account becomes the instance admin.</div>
      </div>
      <div v-if="error" class="error">{{ error }}</div>
      <button class="primary full" type="submit" :disabled="busy">{{ mode === 'login' ? 'Sign in' : 'Create account' }}</button>
      <p class="switch muted small">
        <template v-if="mode === 'login'">No account yet? <a href="#" @click.prevent="mode = 'register'">Create one</a></template>
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
