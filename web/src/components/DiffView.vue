<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { api, type CodeRange, type DiffFile, type DiffLine, type Message, type MergeRequest, type MovedComment } from '../api'
import { useToasts } from '../stores/toast'
import InlineThread from './InlineThread.vue'

// The changes of a merge request, file by file, with review comments under
// the lines they discuss. A comment on an added or unchanged line is pinned
// to the source tip (head), one on a removed line to the merge base, so it
// stays put while the branch moves on. Comments on earlier versions follow
// their lines (moved) while those are unchanged, and are listed separately as
// outdated once they changed.
const props = defineProps<{ mr: MergeRequest; files: DiffFile[]; moved: MovedComment[]; comments: Message[]; canComment: boolean }>()
const emit = defineEmits<{ issue: [m: Message]; links: [m: Message] }>()
const toasts = useToasts()

type Side = 'old' | 'new'
interface Selection {
  file: DiffFile
  side: Side
  start: number
  end: number
}
const selection = ref<Selection | null>(null)
const draft = ref('')
const busy = ref(false)
const collapsed = ref<Record<string, boolean>>({})

const codeOf = (m: Message): CodeRange | null => {
  const a = m.attachments.find((x) => x.type === 'code')
  return a && a.type === 'code' ? a.code : null
}
const key = (path: string, side: Side, line: number) => `${path}\u0000${side}\u0000${line}`

// Comments by the line they end on, and the ones that fit no line anymore.
const placed = computed(() => {
  const at = new Map<string, Message[]>()
  const outdated: Message[] = []
  const lines = new Set<string>()
  for (const f of props.files) {
    for (const h of f.hunks) {
      for (const l of h.lines) for (const k of lineKeys(f, l)) lines.add(k)
    }
  }
  const moved = new Map(props.moved.map((x) => [x.message_id, x]))
  for (const m of props.comments) {
    const c = codeOf(m)
    if (!c) continue
    const mv = moved.get(m.id)
    const side: Side | null = c.commit === props.mr.head_sha ? 'new' : c.commit === props.mr.base_sha ? 'old' : null
    const k = mv ? key(mv.path, mv.side, mv.end) : side && key(c.path, side, c.end)
    if (k && lines.has(k)) at.set(k, [...(at.get(k) ?? []), m])
    else outdated.push(m)
  }
  return { at, outdated }
})

// New comments go on the new side, except on removed lines.
function lineSide(l: DiffLine): [Side, number] {
  return l.kind === 'del' ? ['old', l.old!] : ['new', l.new!]
}
// A comment shows on a line through either side: an unchanged line exists
// on both, so a comment made on the base still finds it.
function lineKeys(f: DiffFile, l: DiffLine) {
  const keys = []
  if (l.kind !== 'add') keys.push(key(f.old_path, 'old', l.old!))
  if (l.kind !== 'del') keys.push(key(f.new_path, 'new', l.new!))
  return keys
}
function threadsAt(f: DiffFile, l: DiffLine) {
  return lineKeys(f, l).flatMap((k) => placed.value.at.get(k) ?? [])
}
function selected(f: DiffFile, l: DiffLine) {
  const s = selection.value
  if (!s || s.file !== f) return false
  const [side, n] = lineSide(l)
  return side === s.side && n >= s.start && n <= s.end
}
function formAfter(f: DiffFile, l: DiffLine) {
  const s = selection.value
  if (!s || s.file !== f) return false
  const [side, n] = lineSide(l)
  return side === s.side && n === s.end
}

// Click a line to comment on it; shift-click another line on the same side
// to comment on the range.
async function pick(f: DiffFile, l: DiffLine, e: MouseEvent) {
  if (!props.canComment) return
  const [side, n] = lineSide(l)
  const s = selection.value
  if (e.shiftKey && s && s.file === f && s.side === side) {
    selection.value = { file: f, side, start: Math.min(s.start, n), end: Math.max(s.start, n) }
  } else {
    selection.value = { file: f, side, start: n, end: n }
  }
  await nextTick()
  document.querySelector<HTMLTextAreaElement>('.comment-form textarea')?.focus()
}
function cancel() {
  selection.value = null
  draft.value = ''
}
async function submit() {
  const s = selection.value
  if (!s || !draft.value.trim()) return
  busy.value = true
  try {
    await api.postMessage(props.mr.channel_id, {
      body: draft.value.trim(),
      code: {
        app: props.mr.app_key,
        repo: props.mr.repo,
        ref: s.side === 'new' ? props.mr.head_sha : props.mr.base_sha,
        path: s.side === 'new' ? s.file.new_path : s.file.old_path,
        start: s.start,
        end: s.end,
      },
    })
    cancel()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) submit()
  if (e.key === 'Escape') cancel()
}

const isMoved = (m: Message) => props.moved.some((x) => x.message_id === m.id)
const fileName = (f: DiffFile) => (f.status === 'deleted' ? f.old_path : f.new_path)
const anchor = (f: DiffFile) => 'file-' + fileName(f).replace(/[^a-zA-Z0-9]+/g, '-')
const totals = computed(() => props.files.reduce((t, f) => ({ add: t.add + f.additions, del: t.del + f.deletions }), { add: 0, del: 0 }))
const commentCount = (f: DiffFile) =>
  props.comments.filter((m) => {
    const c = codeOf(m)
    return c && (c.path === f.new_path || c.path === f.old_path)
  }).length
</script>

<template>
  <div class="diff-view">
    <div class="summary card">
      <div class="row small">
        <strong>{{ files.length }} {{ files.length === 1 ? 'file' : 'files' }} changed</strong>
        <span class="add">+{{ totals.add }}</span>
        <span class="del">−{{ totals.del }}</span>
        <span class="spacer" />
        <span v-if="canComment" class="muted">Click a line to comment, shift-click to comment on a range</span>
      </div>
      <ul class="files">
        <li v-for="f in files" :key="fileName(f)">
          <a :href="'#' + anchor(f)" class="mono">{{ fileName(f) }}</a>
          <span v-if="f.status !== 'modified'" class="badge small">{{ f.status }}</span>
          <span v-if="commentCount(f)" class="small muted">💬 {{ commentCount(f) }}</span>
          <span class="spacer" />
          <span class="add small">+{{ f.additions }}</span>
          <span class="del small">−{{ f.deletions }}</span>
        </li>
      </ul>
    </div>

    <details v-if="placed.outdated.length" class="outdated card">
      <summary>{{ placed.outdated.length }} {{ placed.outdated.length === 1 ? 'comment' : 'comments' }} on an earlier version</summary>
      <div v-for="m in placed.outdated" :key="m.id" class="outdated-item">
        <InlineThread :root="m" outdated @issue="emit('issue', $event)" @links="emit('links', $event)" />
      </div>
    </details>

    <div v-for="f in files" :id="anchor(f)" :key="fileName(f)" class="file card">
      <header>
        <button class="ghost small" :aria-label="collapsed[fileName(f)] ? 'Expand' : 'Collapse'" @click="collapsed[fileName(f)] = !collapsed[fileName(f)]">
          {{ collapsed[fileName(f)] ? '▸' : '▾' }}
        </button>
        <span class="mono path">
          <template v-if="f.status === 'renamed'">{{ f.old_path }} → </template>{{ fileName(f) }}
        </span>
        <span v-if="f.status !== 'modified'" class="badge small">{{ f.status }}</span>
        <span class="spacer" />
        <span class="add small">+{{ f.additions }}</span>
        <span class="del small">−{{ f.deletions }}</span>
        <RouterLink
          v-if="f.status !== 'deleted'"
          class="small"
          :to="`/apps/${mr.app_key}/repos/${mr.repo}/blob/${mr.head_sha}/${f.new_path}`"
        >View file</RouterLink>
      </header>
      <template v-if="!collapsed[fileName(f)]">
        <div v-if="f.binary" class="empty small">Binary file, not shown.</div>
        <div v-else-if="f.too_large" class="empty small">This change is too large to show.</div>
        <div v-else-if="!f.hunks.length" class="empty small">No content changes.</div>
        <table v-else>
          <tbody v-for="(h, hi) in f.hunks" :key="hi">
            <tr class="hunk">
              <td class="ln" />
              <td class="ln" />
              <td class="src">{{ h.header }}</td>
            </tr>
            <template v-for="(l, li) in h.lines" :key="li">
              <tr :class="[l.kind, { sel: selected(f, l), commentable: canComment }]" @click="pick(f, l, $event)">
                <td class="ln">{{ l.old ?? '' }}</td>
                <td class="ln">{{ l.new ?? '' }}</td>
                <td class="src"><span class="sign">{{ l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' ' }}</span>{{ l.text || ' ' }}</td>
              </tr>
              <tr v-for="m in threadsAt(f, l)" :key="m.id" class="thread-row">
                <td colspan="3">
                  <InlineThread :root="m" :earlier="isMoved(m)" @issue="emit('issue', $event)" @links="emit('links', $event)" />
                </td>
              </tr>
              <tr v-if="formAfter(f, l)" class="thread-row">
                <td colspan="3">
                  <div class="comment-form card">
                    <div class="small muted">
                      Comment on {{ selection!.side === 'new' ? fileName(f) : f.old_path }}
                      {{ selection!.start === selection!.end ? `line ${selection!.start}` : `lines ${selection!.start}–${selection!.end}` }}
                      {{ selection!.side === 'old' ? '(removed)' : '' }}
                    </div>
                    <textarea v-model="draft" rows="3" placeholder="Leave a comment, or ask an agent, for example @assistant is this safe?" @keydown="onKey" @click.stop />
                    <div class="row">
                      <button class="primary small" :disabled="busy || !draft.trim()" @click.stop="submit">Comment</button>
                      <button class="ghost small" @click.stop="cancel">Cancel</button>
                      <span class="small muted">Ctrl+Enter to send</span>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </template>
    </div>
  </div>
</template>

<style scoped>
.diff-view { display: flex; flex-direction: column; gap: 12px; }
.summary { padding: 10px 14px; }
.files { list-style: none; margin: 8px 0 0; padding: 0; }
.files li { display: flex; gap: 8px; align-items: center; padding: 2px 0; font-size: 13px; }
.add { color: var(--success); font-weight: 600; }
.del { color: var(--danger); font-weight: 600; }
.outdated { padding: 8px 14px; }
.outdated summary { cursor: pointer; color: var(--muted); }
.outdated-item { margin-top: 8px; }
.file { overflow: hidden; }
.file header {
  display: flex; gap: 8px; align-items: center; padding: 6px 10px; background: var(--surface-2);
  border-bottom: 1px solid var(--border); position: sticky; top: 0; z-index: 1;
}
.path { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
table { border-collapse: collapse; width: 100%; font-family: var(--mono); font-size: 12.5px; tab-size: 4; }
td { padding: 0 10px; line-height: 20px; vertical-align: top; }
.ln { width: 1%; min-width: 44px; text-align: right; color: var(--muted); user-select: none; border-right: 1px solid var(--border); }
.src { white-space: pre-wrap; word-break: break-all; width: 100%; }
.sign { display: inline-block; width: 14px; color: var(--muted); user-select: none; }
tr.add td { background: color-mix(in srgb, var(--success) 12%, transparent); }
tr.del td { background: color-mix(in srgb, var(--danger) 12%, transparent); }
tr.add .ln, tr.del .ln { background: color-mix(in srgb, var(--surface-2) 60%, transparent); }
tr.hunk td { background: var(--accent-soft); color: var(--muted); }
tr.commentable { cursor: pointer; }
tr.commentable:hover .ln { color: var(--accent); }
tr.sel td { background: var(--highlight); }
.thread-row td { padding: 0; background: var(--surface-2); }
.comment-form { margin: 6px 12px 6px 96px; padding: 10px; display: flex; flex-direction: column; gap: 6px; max-width: 760px; font-family: Inter, system-ui, sans-serif; }
</style>
