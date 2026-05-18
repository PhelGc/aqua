// Store reactivo del chat. Usa Composition API plana (sin Pinia) porque el
// estado es chico y solo App.vue lo necesita. Si crece, se migra fácil.
//
// El store traduce los CommandEvent del stream a mutaciones del array de
// ChatMessage que renderiza la UI. La lógica de "qué hacer con cada evento"
// vive acá; los componentes solo leen el array y emiten input.

import { ref, computed } from 'vue'
import { fetchHistory, sendCommand, type CommandStream } from '../api'
import type { ChatMessage, CommandEvent } from '../types'

const messages = ref<ChatMessage[]>([])
const currentStream = ref<CommandStream | null>(null)

let nextId = 1
const newId = () => String(nextId++)

/** True mientras hay un /command en vuelo (input deshabilitado). */
const sending = computed(() => currentStream.value !== null)

/** Envía un mensaje del usuario. Crea el bubble del user, abre el stream y
 *  va apurando los deltas hacia el último bubble del assistant. Tools se
 *  insertan como mensajes propios entre user y assistant.
 *  attachments: IDs de archivos ya subidos via uploadFiles(). */
function send(text: string, attachments: string[] = []) {
  if (sending.value) return
  if (!text.trim() && attachments.length === 0) return

  // Bubble del user lo crea el evento `user` del backend (echo). No lo
  // anticipamos acá para que el orden sea exactamente el del backend.

  // Bubble del assistant placeholder: lo creamos al recibir el primer delta
  // o tool (no antes para no mostrar burbuja vacía si todo falla).
  let assistantId: string | null = null

  const ensureAssistant = (): ChatMessage => {
    if (assistantId !== null) {
      const found = messages.value.find((m) => m.id === assistantId)
      if (found) return found
    }
    const msg: ChatMessage = {
      id: newId(),
      role: 'assistant',
      content: '',
      streaming: true,
    }
    assistantId = msg.id
    messages.value.push(msg)
    return msg
  }

  const handle = sendCommand(text, (evt: CommandEvent) => {
    switch (evt.type) {
      case 'user':
        messages.value.push({
          id: newId(),
          role: 'user',
          content: evt.text,
        })
        break

      case 'delta': {
        const m = ensureAssistant()
        if (evt.content) m.content += evt.content
        if (evt.reasoning) m.reasoning = (m.reasoning ?? '') + evt.reasoning
        break
      }

      case 'tool':
        // Tool va como mensaje propio, NO dentro del assistant. Si ya
        // habíamos empezado el bubble del assistant, lo cerramos y el
        // siguiente delta abre uno nuevo (para que el tool quede entre
        // medio en el orden cronológico real).
        if (assistantId !== null) {
          const m = messages.value.find((x) => x.id === assistantId)
          if (m) m.streaming = false
          assistantId = null
        }
        messages.value.push({
          id: newId(),
          role: 'tool',
          content: evt.name,
        })
        break

      case 'system':
        messages.value.push({
          id: newId(),
          role: 'system',
          content: evt.text,
        })
        break

      case 'error':
        messages.value.push({
          id: newId(),
          role: 'error',
          content: evt.message,
        })
        break

      case 'done':
        if (assistantId !== null) {
          const m = messages.value.find((x) => x.id === assistantId)
          if (m) {
            m.streaming = false
            if (evt.artifact) m.artifact = evt.artifact
            // Si el backend tiene un text definitivo distinto de lo
            // streameado (ej. dispatch de orchestrate consolidado),
            // sobrescribimos.
            if (evt.text && evt.text !== m.content) {
              m.content = evt.text
            }
          }
        }
        // Si nunca hubo assistant pero el done trae text (raro), lo metemos.
        else if (evt.text) {
          messages.value.push({
            id: newId(),
            role: 'assistant',
            content: evt.text,
            artifact: evt.artifact || undefined,
          })
        }
        currentStream.value = null
        break
    }
  }, attachments)
  currentStream.value = handle
  handle.done.finally(() => {
    if (currentStream.value === handle) currentStream.value = null
  })
}

/** Cancela el request en vuelo (si lo hay). Aborta el fetch en el cliente
 *  pero NO le avisa al backend; el agent va a terminar su trabajo igual.
 *  Para cancelar de verdad necesitaríamos un endpoint dedicado. */
function cancel() {
  currentStream.value?.cancel()
  currentStream.value = null
  // Marcamos el último assistant como no-streaming para que se vea quieto.
  for (let i = messages.value.length - 1; i >= 0; i--) {
    if (messages.value[i].role === 'assistant' && messages.value[i].streaming) {
      messages.value[i].streaming = false
      break
    }
  }
}

/** Limpia la vista local. NO toca el historial del backend (para eso
 *  el usuario manda `/reset`). */
function clear() {
  messages.value = []
}

/** Inserta un mensaje sistema en la vista (sin tocar el backend). Útil para
 *  notas contextuales como "sesión cambiada". */
function pushSystem(text: string) {
  messages.value.push({
    id: newId(),
    role: 'system',
    content: text,
  })
}

/** Reemplaza messages con lo que tenga el backend en el history actual.
 *  Solo trae user/assistant — tool-calls y reasoning del pasado se pierden
 *  porque no se persisten en el wire del history. La vista queda "plana"
 *  pero conserva la conversación al refrescar. */
async function loadHistory(): Promise<void> {
  try {
    const h = await fetchHistory()
    messages.value = h.messages.map((m) => ({
      id: newId(),
      role: m.role,
      content: m.content,
    }))
  } catch (e) {
    messages.value = []
    pushSystem('no se pudo cargar el history: ' + (e as Error).message)
  }
}

export function useChat() {
  return { messages, sending, send, cancel, clear, pushSystem, loadHistory }
}
