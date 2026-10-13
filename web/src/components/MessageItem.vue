<script setup lang="ts">
import type { Message } from '../api'
import CodeSnippet from './CodeSnippet.vue'
import EntityChip from './EntityChip.vue'

defineProps<{ message: Message; inThread?: boolean }>()
const emit = defineEmits<{ reply: []; todo: []; issue: []; links: [] }>()

function time(s: string) {
  const d = new Date(s)
  const today = new Date().toDateString() === d.toDateString()
  return today ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}
</script>

<template>
  <div :id="`msg-${message.id}`" class="msg" :class="{ system: !message.author }">
    <div class="avatar">{{ message.author ? message.author.display_name.slice(0, 1).toUpperCase() : '◎' }}</div>
    <div class="content">
      <div class="meta">
        <strong>{{ message.author?.display_name ?? 'Orbit' }}</strong>
        <span class="muted small">{{ time(message.created_at) }}</span>
      </div>
      <div v-if="message.body" class="body">{{ message.body }}</div>
      <template v-for="(a, i) in message.attachments" :key="i">
        <CodeSnippet v-if="a.type === 'code'" :code="a.code" :lines="a.lines" :url="a.url" />
        <div v-else-if="a.type === 'entity'" class="entity">
          <EntityChip :entity="a.entity" />
          <blockquote v-if="a.quote">{{ a.quote }}</blockquote>
        </div>
      </template>
      <div class="footer">
        <button v-if="!inThread && message.reply_count" class="ghost small replies" @click="emit('reply')">
          {{ message.reply_count }} {{ message.reply_count === 1 ? 'reply' : 'replies' }}
        </button>
        <button v-if="message.link_count" class="ghost small" @click="emit('links')">🔗 {{ message.link_count }} linked</button>
      </div>
    </div>
    <div v-if="message.author" class="actions card">
      <button v-if="!inThread" class="ghost small" title="Reply in thread" @click="emit('reply')">Reply</button>
      <button class="ghost small" title="Add to my to-dos" @click="emit('todo')">+ To-do</button>
      <button class="ghost small" title="Create an issue from this conversation" @click="emit('issue')">+ Issue</button>
    </div>
  </div>
</template>

<style scoped>
.msg { display: flex; gap: 10px; padding: 6px 16px; position: relative; border-radius: 6px; }
.msg:hover { background: var(--surface-2); }
.avatar {
  width: 32px; height: 32px; flex-shrink: 0; border-radius: 8px; background: var(--accent); color: white;
  display: flex; align-items: center; justify-content: center; font-weight: 650; margin-top: 2px;
}
.system .avatar { background: var(--surface-2); color: var(--accent); border: 1px solid var(--border); }
.system .body { color: var(--muted); }
.content { flex: 1; min-width: 0; }
.meta { display: flex; gap: 8px; align-items: baseline; }
.body { white-space: pre-wrap; word-wrap: break-word; }
.entity { margin-top: 4px; }
blockquote { margin: 6px 0 0; padding: 4px 10px; border-left: 3px solid var(--accent); color: var(--muted); white-space: pre-wrap; }
.footer { display: flex; gap: 4px; margin-left: -8px; }
.footer button { color: var(--accent); }
.actions { position: absolute; top: 2px; right: 16px; display: none; padding: 2px; gap: 2px; box-shadow: 0 2px 8px #0002; }
.msg:hover .actions { display: flex; }
</style>
