import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, type Ref, type Todo } from '../api'
import { subscribe } from '../realtime'

// Todos live in a store so the sidebar count and the todo page stay in sync,
// including across tabs through realtime events.
export const useTodos = defineStore('todos', () => {
  const items = ref<Todo[]>([])
  const open = computed(() => items.value.filter((t) => !t.done))
  let unsub: (() => void) | null = null

  function upsert(t: Todo) {
    const i = items.value.findIndex((x) => x.id === t.id)
    if (i >= 0) items.value[i] = t
    else items.value.unshift(t)
  }

  async function load(userId: string) {
    items.value = await api.todos()
    unsub?.()
    unsub = subscribe('user:' + userId, (type, data) => {
      if (type === 'todo.updated') upsert(data)
      if (type === 'todo.deleted') items.value = items.value.filter((t) => t.id !== data.id)
    })
  }
  async function add(input: { title?: string; source?: Ref }) {
    const t = await api.createTodo(input)
    upsert(t)
    return t
  }
  async function toggle(t: Todo) {
    upsert(await api.updateTodo(t.id, { done: !t.done }))
  }
  async function remove(t: Todo) {
    await api.deleteTodo(t.id)
    items.value = items.value.filter((x) => x.id !== t.id)
  }
  return { items, open, load, add, toggle, remove }
})
