<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import EntityChip from '../components/EntityChip.vue'
import { useToasts } from '../stores/toast'
import { useTodos } from '../stores/todos'

const todos = useTodos()
const toasts = useToasts()
const router = useRouter()
const title = ref('')
const showDone = ref(false)
const done = computed(() => todos.items.filter((t) => t.done))

async function add() {
  if (!title.value.trim()) return
  try {
    await todos.add({ title: title.value })
    title.value = ''
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>To-dos</h1>
        <div class="sub">Personal and private. Open one to jump straight back to where it came from.</div>
      </div>
    </div>
    <form class="row add" @submit.prevent="add">
      <input v-model="title" placeholder="Add a to-do" />
      <button class="primary" type="submit" :disabled="!title.trim()">Add</button>
    </form>
    <div class="card">
      <ul v-if="todos.open.length" class="list">
        <li v-for="t in todos.open" :key="t.id" class="todo">
          <input type="checkbox" class="check" :checked="t.done" :aria-label="`Mark ${t.title} done`" @change="todos.toggle(t)" />
          <div class="grow">
            <a v-if="t.source" href="#" class="title" @click.prevent="router.push(t.source.url)">{{ t.title }}</a>
            <span v-else class="title">{{ t.title }}</span>
            <EntityChip v-if="t.source" :entity="t.source" />
          </div>
          <button class="ghost small" title="Delete" @click="todos.remove(t)">Delete</button>
        </li>
      </ul>
      <div v-else class="empty">All done.</div>
    </div>
    <h2 v-if="done.length">
      <a href="#" @click.prevent="showDone = !showDone">{{ showDone ? 'Hide' : 'Show' }} {{ done.length }} done</a>
    </h2>
    <div v-if="showDone && done.length" class="card">
      <ul class="list">
        <li v-for="t in done" :key="t.id" class="todo done">
          <input type="checkbox" class="check" checked @change="todos.toggle(t)" />
          <div class="grow">
            <span class="title">{{ t.title }}</span>
            <EntityChip v-if="t.source" :entity="t.source" />
          </div>
          <button class="ghost small" @click="todos.remove(t)">Delete</button>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.add { margin-bottom: 16px; max-width: 640px; }
.check { width: auto; }
.grow { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; align-items: flex-start; }
.title { font-weight: 500; color: var(--text); }
a.title:hover { color: var(--accent); }
.done .title { text-decoration: line-through; color: var(--muted); }
</style>
