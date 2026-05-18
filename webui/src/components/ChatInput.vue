<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'

const props = defineProps<{
  /** True mientras hay un request en vuelo (deshabilita Enter). */
  sending: boolean
}>()
const emit = defineEmits<{
  submit: [text: string]
  cancel: []
}>()

const text = ref('')
const ta = ref<HTMLTextAreaElement | null>(null)

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
  if (!t || props.sending) return
  emit('submit', t)
  text.value = ''
  nextTick(autosize)
}
</script>

<template>
  <div class="input-wrap">
    <textarea
      ref="ta"
      v-model="text"
      :placeholder="sending ? 'aqua está respondiendo… (esc cancela)' : 'escribí un mensaje o /skill ...'"
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
      >
        ✕ cancelar
      </button>
      <button
        v-else
        type="button"
        class="send"
        :disabled="!text.trim()"
        @click="submit"
        title="Enviar (Enter)"
      >
        enviar ↵
      </button>
    </div>
  </div>
</template>

<style scoped>
.input-wrap {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  background: var(--bg-elev);
}
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
button {
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
button:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
button.cancel {
  background: transparent;
  color: var(--error);
  border: 1px solid var(--error);
}
button.cancel:hover { background: rgba(248, 113, 113, 0.1); }
button.send:hover:not(:disabled) { opacity: 0.85; }
</style>
