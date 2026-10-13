import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type Agent } from '../api'

// The agents on this instance, loaded once. The composer uses them to notice
// when a message mentions an agent.
export const useAgents = defineStore('agents', () => {
  const items = ref<Agent[]>([])
  let loading: Promise<void> | null = null

  function load() {
    loading ??= api.agents().then((a) => {
      items.value = a.sort((x, y) => x.name.localeCompare(y.name))
    }).catch(() => {
      loading = null
    })
    return loading
  }

  // mentioned returns the agents a message body @mentions.
  function mentioned(body: string): Agent[] {
    const names = new Set([...body.toLowerCase().matchAll(/(?:^|[^a-z0-9._@/-])@([a-z0-9][a-z0-9._-]*)/g)].map((m) => m[1].replace(/[._-]+$/, '')))
    return items.value.filter((a) => names.has(a.name))
  }

  return { items, load, mentioned }
})
