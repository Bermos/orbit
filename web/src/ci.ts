import type { Pipeline } from './api'

export function ago(s: string, now = Date.now()) {
  const sec = Math.max(0, Math.round((now - new Date(s).getTime()) / 1000))
  if (sec < 60) return 'just now'
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`
  if (sec < 7 * 86400) return `${Math.floor(sec / 86400)} d ago`
  return new Date(s).toLocaleDateString()
}

export function duration(start: string | null, end: string | null, now = Date.now()) {
  if (!start) return ''
  const sec = Math.max(0, Math.round(((end ? new Date(end).getTime() : now) - new Date(start).getTime()) / 1000))
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

export function triggerText(p: Pipeline) {
  const who = p.triggered_by ? '@' + p.triggered_by.username : 'someone'
  if (p.trigger === 'agent') {
    const agent = p.jobs.find((j) => j.kind === 'agent')?.spec.agent
    return `${agent ? '@' + agent : 'agent'} for ${who}`
  }
  return `${p.trigger} by ${who}`
}

// An in-app path for a URL on this instance, or null for external links.
export function internalPath(url: string): string | null {
  if (url.startsWith('/') && !url.startsWith('//')) return url
  if (url.startsWith(location.origin + '/')) return url.slice(location.origin.length)
  return null
}

export const active = (status: string) => status === 'pending' || status === 'running'
