// One WebSocket per tab. Components subscribe to topics ("channel:<id>",
// "user:<id>") and get events pushed as they happen.

type Handler = (type: string, data: any) => void

const handlers = new Map<string, Set<Handler>>()
let ws: WebSocket | null = null
let retry = 0

function send(action: 'subscribe' | 'unsubscribe', topics: string[]) {
  if (ws?.readyState === WebSocket.OPEN && topics.length) ws.send(JSON.stringify({ action, topics }))
}

function connect() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  ws = new WebSocket(`${proto}//${location.host}/api/v1/events`)
  ws.onopen = () => {
    retry = 0
    send('subscribe', [...handlers.keys()])
  }
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data)
    handlers.get(msg.topic)?.forEach((h) => h(msg.type, msg.data))
  }
  ws.onclose = () => {
    ws = null
    setTimeout(connect, Math.min(1000 * 2 ** retry++, 15000))
  }
}

export function startRealtime() {
  if (!ws) connect()
}

export function subscribe(topic: string, handler: Handler): () => void {
  let set = handlers.get(topic)
  if (!set) {
    set = new Set()
    handlers.set(topic, set)
    send('subscribe', [topic])
  }
  set.add(handler)
  return () => {
    set!.delete(handler)
    if (set!.size === 0) {
      handlers.delete(topic)
      send('unsubscribe', [topic])
    }
  }
}
