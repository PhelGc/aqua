<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { uploadFiles } from '../api'
import type { AttachmentMeta } from '../types'

const props = defineProps<{
  /** True mientras hay un request en vuelo (deshabilita Enter y upload). */
  sending: boolean
}>()
const emit = defineEmits<{
  submit: [text: string, attachments: string[]]
  cancel: []
}>()

const text = ref('')
const ta = ref<HTMLTextAreaElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const pending = ref<AttachmentMeta[]>([])
const uploading = ref(false)
const uploadError = ref<string | null>(null)
const dragOver = ref(false)

// Autosize: el textarea crece con el contenido hasta un máximo.
function autosize() {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  const maxH = 240
  el.style.height = Math.min(el.scrollHeight, maxH) + 'px'
}
watch(text, () => nextTick(autosize))

function onKeydown(e: KeyboardEvent) {
  // Enter envía. Shift+Enter o Ctrl+Enter inserta línea nueva.
  if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault()
    submit()
  } else if (e.key === 'Escape' && props.sending) {
    e.preventDefault()
    emit('cancel')
  }
}

function submit() {
  const t = text.value.trim()
  if (props.sending || uploading.value) return
  if (!t && pending.value.length === 0) return
  emit(
    'submit',
    t,
    pending.value.map((p) => p.id),
  )
  text.value = ''
  pending.value = []
  uploadError.value = null
  nextTick(autosize)
}

async function handleFiles(files: FileList | File[]) {
  if (files.length === 0 || uploading.value || props.sending) return
  uploading.value = true
  uploadError.value = null
  try {
    const arr = Array.from(files)
    const metas = await uploadFiles(arr)
    pending.value.push(...metas)
  } catch (e) {
    uploadError.value = (e as Error).message
  } finally {
    uploading.value = false
  }
}

function onFilePick(e: Event) {
  const input = e.target as HTMLInputElement
  if (input.files) handleFiles(input.files)
  input.value = '' // reset para poder elegir el mismo archivo de nuevo
}

function removePending(id: string) {
  pending.value = pending.value.filter((p) => p.id !== id)
}

// ─── Drag & drop sobre toda el área del chat ─────────────────────────────────
// Los eventos los recibe la raíz <div>; cualquier hijo (textarea, botones) los
// hereda porque no llamamos stopPropagation en los hijos.
function onDragEnter(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragOver.value = true
}
function onDragOver(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragOver.value = true
}
function onDragLeave(e: DragEvent) {
  // dragleave dispara también cuando el cursor pasa entre hijos. Chequeamos
  // que esté saliendo del root real (relatedTarget fuera del root).
  const root = e.currentTarget as HTMLElement
  if (!root.contains(e.relatedTarget as Node)) {
    dragOver.value = false
  }
}
function onDrop(e: DragEvent) {
  e.preventDefault()
  dragOver.value = false
  if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
    handleFiles(e.dataTransfer.files)
  }
}
function hasFiles(e: DragEvent): boolean {
  return !!e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files')
}

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function kindIcon(kind: string): string {
  switch (kind) {
    case 'xlsx': return '📊'
    case 'csv':
    case 'tsv': return '📋'
    case 'pdf': return '📕'
    case 'image': return '🖼'
    case 'text': return '📄'
    default: return '📎'
  }
}
</script>

<template>
  <div
    class="input-wrap"
    :class="{ 'drag-over': dragOver }"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <!-- Chips de archivos pendientes encima del textarea. -->
    <div v-if="pending.length > 0 || uploading || uploadError" class="chips">
      <span
        v-for="p in pending"
        :key="p.id"
        class="chip"
        :title="`${p.name} · ${humanSize(p.size)}`"
      >
        <span class="chip-icon">{{ kindIcon(p.kind) }}</span>
        <span class="chip-name">{{ p.name }}</span>
        <span class="chip-size">{{ humanSize(p.size) }}</span>
        <button
          type="button"
          class="chip-del"
          :disabled="sending"
          title="quitar"
          @click="removePending(p.id)"
        >×</button>
      </span>
      <span v-if="uploading" class="chip uploading mono">subiendo…</span>
      <span v-if="uploadError" class="chip error mono">⚠ {{ uploadError }}</span>
    </div>

    <div class="row">
      <button
        type="button"
        class="attach"
        :disabled="sending || uploading"
        title="adjuntar archivos"
        @click="fileInput?.click()"
      >📎</button>
      <input
        ref="fileInput"
        type="file"
        multiple
        style="display: none"
        @change="onFilePick"
      />
      <textarea
        ref="ta"
        v-model="text"
        :placeholder="sending ? 'aqua está respondiendo… (esc cancela)' : 'escribí un mensaje o arrastrá archivos…'"
        rows="1"
        @keydown="onKeydown"
      />
      <div class="actions">
        <button
          v-if="sending"
          type="button"
          class="cancel"
          @click="emit('cancel')"
          title="Cancelar (Esc)"
        >✕ cancelar</button>
        <button
          v-else
          type="button"
          class="send"
          :disabled="(!text.trim() && pending.length === 0) || uploading"
          @click="submit"
          title="Enviar (Enter)"
        >enviar ↵</button>
      </div>
    </div>

    <!-- Overlay visible cuando estás arrastrando. -->
    <div v-if="dragOver" class="drop-overlay">
      <div class="drop-card">
        <div class="drop-icon">📂</div>
        <div>soltá los archivos para adjuntar</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.input-wrap {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  background: var(--bg-elev);
  position: relative;
}
.input-wrap.drag-over {
  background: rgba(125, 211, 252, 0.05);
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  max-width: 280px;
}
.chip-icon { font-size: 14px; }
.chip-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.chip-size {
  color: var(--fg-dim);
  font-size: 11px;
}
.chip-del {
  background: transparent;
  border: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 14px;
  padding: 0 2px;
  line-height: 1;
}
.chip-del:hover { color: var(--error); }
.chip-del:disabled { cursor: not-allowed; opacity: 0.4; }
.chip.uploading { color: var(--fg-dim); }
.chip.error { color: var(--error); border-color: var(--error); }

.row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
.attach {
  background: transparent;
  color: var(--fg-dim);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 16px;
  cursor: pointer;
  height: 38px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.attach:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.attach:disabled { opacity: 0.4; cursor: not-allowed; }

textarea {
  flex: 1;
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 10px 12px;
  font-family: inherit;
  font-size: 14px;
  line-height: 1.4;
  resize: none;
  outline: none;
  min-height: 38px;
  max-height: 240px;
  transition: border-color 120ms;
}
textarea:focus { border-color: var(--accent); }
textarea::placeholder { color: var(--fg-dim); }

.actions {
  display: flex;
  gap: 6px;
}
.actions button {
  background: var(--accent);
  color: #0f172a;
  border: none;
  border-radius: 6px;
  padding: 9px 14px;
  font-weight: 600;
  font-size: 13px;
  cursor: pointer;
  font-family: inherit;
  transition: opacity 120ms;
}
.actions button:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.actions button.cancel {
  background: transparent;
  color: var(--error);
  border: 1px solid var(--error);
}
.actions button.cancel:hover { background: rgba(248, 113, 113, 0.1); }
.actions button.send:hover:not(:disabled) { opacity: 0.85; }

.drop-overlay {
  position: absolute;
  inset: 0;
  background: rgba(15, 23, 42, 0.85);
  border: 2px dashed var(--accent);
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}
.drop-card {
  text-align: center;
  color: var(--accent);
  font-weight: 600;
}
.drop-icon { font-size: 32px; margin-bottom: 8px; }
</style>
