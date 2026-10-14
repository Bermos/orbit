import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type Channel } from '../api'
import { subscribe } from '../realtime'

// The signed-in person's private chats with agents, most recent first. The
// sidebar lists them; new messages move a chat to the top, in every tab.
export const useChats = defineStore('chats', () => {
  const items = ref<Channel[]>([])
  let unsub: (() => void) | null = null

  function upsert(c: Channel) {
    items.value = [c, ...items.value.filter((x) => x.id !== c.id)]
  }

  async function load(userId: string) {
    items.value = await api.directChats()
    unsub?.()
    unsub = subscribe('user:' + userId, (type, data) => {
      if (type === 'direct.updated') upsert(data)
    })
  }
  async function start(agent: string) {
    const c = await api.createDirectChat(agent)
    upsert(c)
    return c
  }
  async function remove(id: string) {
    await api.deleteDirectChat(id)
    items.value = items.value.filter((c) => c.id !== id)
  }

  return { items, load, start, remove }
})

// chatTitle is how a chat is named before its first message gives it a title.
export function chatTitle(c: Channel) {
  return c.name || 'New chat'
}
