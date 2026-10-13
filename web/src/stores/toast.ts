import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast {
  id: number
  text: string
  link?: string
  kind: 'info' | 'error'
}

export const useToasts = defineStore('toasts', () => {
  const items = ref<Toast[]>([])
  let next = 1
  function show(text: string, opts: { link?: string; kind?: 'info' | 'error' } = {}) {
    const t = { id: next++, text, link: opts.link, kind: opts.kind ?? 'info' }
    items.value.push(t)
    setTimeout(() => (items.value = items.value.filter((x) => x.id !== t.id)), 5000)
  }
  function error(e: unknown) {
    show(e instanceof Error ? e.message : String(e), { kind: 'error' })
  }
  return { items, show, error }
})
