<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { fetchSkills, uploadFiles } from '../api'
import type { AttachmentMeta, SkillMeta } from '../types'

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

// ─── Autocomplete de skills ──────────────────────────────────────────────────
// Se abre cuando el texto empieza con "/" y el cursor todavía está sobre el
// nombre del comando (antes del primer espacio). Filtra el catálogo de skills
// por prefijo del nombre.

const skills = ref<SkillMeta[]>([])
const skillsLoaded = ref(false)
const showMenu = ref(false)
const activeIdx = ref(0)

onMounted(async () => {
  try {
    skills.value = await fetchSkills()
  } catch {
    // Si falla, no rompemos el input — solo no hay autocomplete.
    skills.value = []
  } finally {
    skillsLoaded.value = true
  }
})

/** Parse del input: detecta si el cursor está sobre un slash-command al inicio.
 *  Devuelve el prefijo (lo que va después del `/` hasta el primer espacio) o
 *  null si no aplica. */
function slashPrefix(): string | null {
  const t = text.value
  if (!t.startsWith('/')) return null
  // Si ya hay un espacio, el usuario está escribiendo args — no autocomplete.
  const sp = t.indexOf(' ')
  if (sp !== -1) return null
  return t.slice(1)
}

const filtered = computed<SkillMeta[]>(() => {
  const prefix = slashPrefix()
  if (prefix === null) return []
  // Normalizamos igual que el backend: lowercase + sin acentos comunes.
  const norm = normalize(prefix)
  const list = skills.value.filter((s) => normalize(s.name).startsWith(norm))
  // Si el prefijo está vacío ("/"), mostramos todas.
  if (prefix === '') return skills.value.slice(0, 20)
  return list.slice(0, 20)
})

function normalize(s: string): string {
  return s
    .toLowerCase()
    .replace(/[áä]/g, 'a')
    .replace(/[éë]/g, 'e')
    .replace(/[íï]/g, 'i')
    .replace(/[óö]/g, 'o')
    .replace(/[úü]/g, 'u')
    .replace(/ñ/g, 'n')
}

// Watch del texto: abre/cierra el menú y resetea el highlight.
watch(text, () => {
  nextTick(autosize)
  const prefix = slashPrefix()
  if (prefix !== null && skillsLoaded.value) {
    showMenu.value = filtered.value.length > 0
    activeIdx.value = 0
  } else {
    showMenu.value = false
  }
})

function pickSkill(s: SkillMeta) {
  // Reemplazamos el slash-command tipeado por "/name " para que el usuario
  // siga con los args. Si no hay args para esta skill, dejamos solo "/name".
  text.value = '/' + s.name + ' '
  showMenu.value = false
  nextTick(() => {
    ta.value?.focus()
    // Cursor al final.
    const el = ta.value
    if (el) {
      el.selectionStart = el.selectionEnd = el.value.length
    }
  })
}

// Autosize: el textarea crece con el contenido hasta un máximo.
function autosize() {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  const maxH = 240
  el.style.height = Math.min(el.scrollHeight, maxH) + 'px'
}

function onKeydown(e: KeyboardEvent) {
  // Si el menú está abierto, capturamos las teclas de navegación.
  if (showMenu.value && filtered.value.length > 0) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      activeIdx.value = (activeIdx.value + 1) % filtered.value.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      activeIdx.value =
        (activeIdx.value - 1 + filtered.value.length) % filtered.value.length
      return
    }
    if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey)) {
      e.preventDefault()
      pickSkill(filtered.value[activeIdx.value])
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      showMenu.value = false
      return
    }
  }

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
  showMenu.value = false
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
    <!-- Menú de autocomplete de skills, sobre el textarea. -->
    <div v-if="showMenu && filtered.length > 0" class="skill-menu">
      <div class="skill-menu-header mono">
        skills · ↑↓ navegar · ⏎/⇥ elegir · esc cerrar
      </div>
      <button
        v-for="(s, i) in filtered"
        :key="s.name"
        type="button"
        class="skill-item"
        :class="{ active: i === activeIdx }"
        @mousedown.prevent="pickSkill(s)"
        @mouseenter="activeIdx = i"
      >
        <span class="skill-name mono">/{{ s.name }}</span>
        <span class="skill-desc">{{ s.description || '(sin descripción)' }}</span>
      </button>
    </div>

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
        :placeholder="sending ? 'aqua está respondiendo… (esc cancela)' : 'escribí un mensaje, / para skills, arrastrá archivos…'"
        rows="1"
        @keydown="onKeydown"
        @blur="showMenu = false"
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

/* Menú flotante por encima del textarea. */
.skill-menu {
  position: absolute;
  left: 16px;
  right: 16px;
  bottom: calc(100% - 8px);
  max-height: 280px;
  overflow-y: auto;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
  z-index: 20;
  padding: 4px;
}
.skill-menu-header {
  padding: 6px 10px;
  font-size: 11px;
  color: var(--fg-dim);
  border-bottom: 1px solid var(--border);
  margin-bottom: 4px;
}
.skill-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  width: 100%;
  padding: 8px 10px;
  background: transparent;
  border: none;
  border-radius: 6px;
  text-align: left;
  cursor: pointer;
  color: var(--fg);
  font-family: inherit;
  font-size: 13px;
}
.skill-item:hover,
.skill-item.active {
  background: rgba(125, 211, 252, 0.12);
}
.skill-name {
  color: var(--accent);
  font-weight: 600;
  font-size: 13px;
}
.skill-desc {
  color: var(--fg-dim);
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
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
