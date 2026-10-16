import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type RegisterInput, type User } from '../api'

export const useSession = defineStore('session', () => {
  const user = ref<User | null>(null)
  const loaded = ref(false)

  async function load() {
    try {
      user.value = await api.me()
    } catch {
      user.value = null
    }
    loaded.value = true
  }
  async function login(username: string, password: string) {
    user.value = (await api.login(username, password)).user
  }
  async function register(input: RegisterInput) {
    user.value = (await api.register(input)).user
  }
  async function logout() {
    await api.logout()
    user.value = null
  }
  return { user, loaded, load, login, register, logout }
})
