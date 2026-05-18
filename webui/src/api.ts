// Cliente del backend aqua. Encapsula:
//  - POST /command como stream SSE (fetch + ReadableStream; EventSource no
//    soporta POST).
//  - GET /events como EventSource estándar (eventos runtime asincrónicos).
//  - GET /api/state como JSON simple.

import type {
  ApiState,
  AttachmentMeta,
  CommandEvent,
  RuntimeEvent,
  SessionsList,
} from './types'

/** Llamada GET /api/state. */
export async function fetchState(): Promise<ApiState> {
  const r = await fetch('/api/state')
  if (!r.ok) throw new Error(`/api/state: HTTP ${r.status}`)
  return r.json()
}

// ─── Sesiones ────────────────────────────────────────────────────────────────

/** GET /api/sessions: lista sesiones + activa. */
export async function fetchSessions(): Promise<SessionsList> {
  const r = await fetch('/api/sessions')
  if (!r.ok) throw new Error(`/api/sessions: HTTP ${r.status}`)
  return r.json()
}

/** POST /api/sessions/switch */
export async function switchSession(name: string): Promise<void> {
  const r = await fetch('/api/sessions/switch', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!r.ok) throw new Error(await r.text().catch(() => `HTTP ${r.status}`))
}

/** POST /api/sessions/new */
export async function newSession(name: string): Promise<void> {
  const r = await fetch('/api/sessions/new', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!r.ok) throw new Error(await r.text().catch(() => `HTTP ${r.status}`))
}

/** DELETE /api/sessions/<name> */
export async function deleteSession(name: string): Promise<void> {
  const r = await fetch(`/api/sessions/${encodeURIComponent(name)}`, { method: 'DELETE' })
  if (!r.ok) throw new Error(await r.text().catch(() => `HTTP ${r.status}`))
}

// ─── Attachments ─────────────────────────────────────────────────────────────

/** Sube archivos al backend. Devuelve la metadata de cada uno. */
export async function uploadFiles(files: File[]): Promise<AttachmentMeta[]> {
  const fd = new FormData()
  for (const f of files) fd.append('file', f, f.name)
  const r = await fetch('/upload', { method: 'POST', body: fd })
  if (!r.ok) throw new Error(await r.text().catch(() => `HTTP ${r.status}`))
  return r.json()
}

/** Abre el stream de eventos asincrónicos del runtime (schedules, jobs, etc.).
 *  Devuelve el EventSource para que el caller pueda cerrarlo. */
export function subscribeRuntime(onEvent: (evt: RuntimeEvent) => void): EventSource {
  const es = new EventSource('/events')
  es.onmessage = (e) => {
    try {
      onEvent(JSON.parse(e.data))
    } catch {
      // Evento mal formado: ignorar.
    }
  }
  return es
}

/** Resultado de sendCommand. */
export interface CommandStream {
  /** Cancela el request (aborta el fetch). */
  cancel: () => void
  /** Promesa que resuelve cuando el stream cierra (normal o por error). */
  done: Promise<void>
}

/** Envía un POST /command y entrega cada evento SSE al callback.
 *  attachments: IDs de uploads previos a prependerar al prompt.
 *  Devuelve un handle con .cancel() para abortar el request. */
export function sendCommand(
  text: string,
  onEvent: (evt: CommandEvent) => void,
  attachments: string[] = [],
): CommandStream {
  const ac = new AbortController()
  const done = (async () => {
    let resp: Response
    try {
      resp = await fetch('/command', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ text, attachments }),
        signal: ac.signal,
      })
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        onEvent({ type: 'error', message: (err as Error).message })
        onEvent({ type: 'done', text: '', artifact: '' })
      }
      return
    }
    if (!resp.ok || !resp.body) {
      const msg = await resp.text().catch(() => `HTTP ${resp.status}`)
      onEvent({ type: 'error', message: msg })
      onEvent({ type: 'done', text: '', artifact: '' })
      return
    }
    await readSSE(resp.body, onEvent, ac.signal)
  })()
  return {
    cancel: () => ac.abort(),
    done,
  }
}

// readSSE consume el ReadableStream del response, parsea el formato SSE
// (event: <name>\ndata: <json>\n\n) y dispatcha al callback con type tagueado.
async function readSSE(
  body: ReadableStream<Uint8Array>,
  onEvent: (evt: CommandEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder('utf-8')
  let buffer = ''

  try {
    while (true) {
      if (signal.aborted) {
        reader.cancel().catch(() => {})
        return
      }
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      // SSE delimita eventos con \n\n.
      let sep: number
      while ((sep = buffer.indexOf('\n\n')) !== -1) {
        const raw = buffer.slice(0, sep)
        buffer = buffer.slice(sep + 2)
        dispatchSSEEvent(raw, onEvent)
      }
    }
    // Por las dudas, drenamos lo que quede en buffer (sin \n\n final).
    if (buffer.trim()) dispatchSSEEvent(buffer, onEvent)
  } catch (err) {
    if ((err as Error).name !== 'AbortError') {
      onEvent({ type: 'error', message: (err as Error).message })
      onEvent({ type: 'done', text: '', artifact: '' })
    }
  }
}

// dispatchSSEEvent parsea un bloque SSE (líneas `event:` y `data:`) y emite
// el CommandEvent correspondiente. Tolera eventos malformados saltándolos.
function dispatchSSEEvent(
  raw: string,
  onEvent: (evt: CommandEvent) => void,
): void {
  let eventType = 'message'
  let dataStr = ''
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) {
      eventType = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      // SSE permite múltiples data: por evento (se concatenan con \n).
      // En la práctica nuestro backend manda uno solo por evento.
      dataStr += (dataStr ? '\n' : '') + line.slice(5).trim()
    }
  }
  if (!dataStr) return
  let payload: any
  try {
    payload = JSON.parse(dataStr)
  } catch {
    return
  }
  // El backend usa event-name; convertimos a discriminated union por type.
  switch (eventType) {
    case 'user':
      onEvent({ type: 'user', text: String(payload.text ?? '') })
      break
    case 'delta':
      onEvent({
        type: 'delta',
        content: payload.content,
        reasoning: payload.reasoning,
      })
      break
    case 'tool':
      onEvent({ type: 'tool', name: String(payload.name ?? '') })
      break
    case 'system':
      onEvent({ type: 'system', text: String(payload.text ?? '') })
      break
    case 'error':
      onEvent({ type: 'error', message: String(payload.message ?? '') })
      break
    case 'done':
      onEvent({
        type: 'done',
        text: String(payload.text ?? ''),
        artifact: String(payload.artifact ?? ''),
      })
      break
  }
}
