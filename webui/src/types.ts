// Tipos del contrato entre el frontend y el backend Go.
// Espejo de las shapes que emite internal/transport/web/web.go.

/** Estado global retornado por GET /api/state. */
export interface ApiState {
  busy: boolean
  session: string
  messages: number
}

// ─── Sesiones (GET /api/sessions) ────────────────────────────────────────────

export interface SessionItem {
  name: string
  /** Cantidad de mensajes persistidos. -1 si la sesión no se pudo leer. */
  messages: number
}

export interface SessionsList {
  current: string
  items: SessionItem[]
}

// ─── History (GET /api/history) ──────────────────────────────────────────────

export interface HistoryMessage {
  role: 'user' | 'assistant'
  content: string
}

export interface HistoryResponse {
  session: string
  messages: HistoryMessage[]
}

// ─── Attachments (POST /upload) ──────────────────────────────────────────────

export type AttachmentKind = 'xlsx' | 'csv' | 'tsv' | 'pdf' | 'text' | 'image' | 'unknown'

export interface AttachmentMeta {
  id: string
  name: string
  size: number
  kind: AttachmentKind
}

/** Eventos asincrónicos del runtime que vienen por GET /events.
 *  Estos son del FanoutSink: schedules, jobs del orchestrator, etc.
 *  Los tool-calls del turn actual vienen por /command, no por acá. */
export interface RuntimeEvent {
  type: string
  time: string
  job_id?: string
  payload?: Record<string, unknown>
}

// ─── Eventos del stream de POST /command ─────────────────────────────────────
// El backend emite un text/event-stream con event types nombrados.
// Cada uno tiene su payload JSON propio.

export interface CommandEventUser {
  type: 'user'
  text: string
}

export interface CommandEventDelta {
  type: 'delta'
  content?: string
  reasoning?: string
}

export interface CommandEventTool {
  type: 'tool'
  name: string
}

export interface CommandEventSystem {
  type: 'system'
  text: string
}

export interface CommandEventError {
  type: 'error'
  message: string
}

export interface CommandEventDone {
  type: 'done'
  text: string
  artifact: string
}

export type CommandEvent =
  | CommandEventUser
  | CommandEventDelta
  | CommandEventTool
  | CommandEventSystem
  | CommandEventError
  | CommandEventDone

// ─── Modelo del chat en la UI ────────────────────────────────────────────────
// Lo que renderiza la vista. Mapea 1:1 con los eventos pero acumula.

export type ChatRole = 'user' | 'assistant' | 'tool' | 'system' | 'error'

export interface ChatMessage {
  /** ID estable para el v-for. */
  id: string
  role: ChatRole
  /** Contenido visible del mensaje. Para `assistant` se va concatenando con
   *  cada `delta.content`. Para `tool` es el nombre. Para `error` el mensaje. */
  content: string
  /** Solo para `assistant`: razonamiento acumulado en vivo (delta.reasoning).
   *  Cuando el turn cierra, este bloque queda colapsable. */
  reasoning?: string
  /** Solo para `assistant`: path del artifact (reporte .md) si vino en done. */
  artifact?: string
  /** Solo para `assistant`: true mientras todavía está llegando el stream. */
  streaming?: boolean
}
