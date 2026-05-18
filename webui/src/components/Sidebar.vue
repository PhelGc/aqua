<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  fetchSessions,
  switchSession,
  newSession,
  deleteSession,
} from '../api'
import type { SessionsList } from '../types'

const emit = defineEmits<{
  /** Se dispara cuando cambiamos de sesión activa (switch o new).
   *  El padre limpia el chat y muestra un mensaje de contexto. */
  switched: [name: string, messages: number]
}>()

const data = ref<SessionsList | null>(null)
const err = ref<string | null>(null)
const loading = ref(false)
const creating = ref(false)
const newName = ref('')

async function reload() {
  try {
    data.value = await fetchSessions()
    err.value = null
  } catch (e) {
    err.value = (e as Error).message
  }
}

async function onSwitch(name: string) {
  if (!data.value || name === data.value.current || loading.value) return
  loading.value = true
  err.value = null
  try {
    await switchSession(name)
    await reload()
    const item = data.value?.items.find((x) => x.name === name)
    emit('switched', name, item?.messages ?? 0)
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function onCreate() {
  const name = newName.value.trim()
  if (!name || loading.value) return
  loading.value = true
  err.value = null
  try {
    await newSession(name)
    newName.value = ''
    creating.value = false
    await reload()
    emit('switched', name, 0)
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function onDelete(name: string) {
  if (loading.value) return
  if (!confirm(`¿Borrar la sesión "${name}"?`)) return
  loading.value = true
  err.value = null
  try {
    await deleteSession(name)
    await reload()
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(reload)
defineExpose({ reload })
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <span class="brand-name">aqua</span>
      <span class="brand-tag mono">web</span>
    </div>

    <div class="section-head">
      <span class="section-label">SESIONES</span>
      <button
        type="button"
        class="icon-btn"
        :title="creating ? 'cancelar' : 'nueva sesión'"
        @click="creating = !creating; newName = ''"
      >{{ creating ? '×' : '+' }}</button>
    </div>

    <form v-if="creating" class="new-form" @submit.prevent="onCreate">
      <input
        v-model="newName"
        placeholder="nombre"
        autofocus
        :disabled="loading"
        class="mono"
      />
      <button type="submit" :disabled="!newName.trim() || loading">crear</button>
    </form>

    <div class="session-list">
      <div
        v-for="item in data?.items ?? []"
        :key="item.name"
        :class="['session-item', { active: item.name === data?.current }]"
        @click="onSwitch(item.name)"
      >
        <span class="session-name mono">{{ item.name }}</span>
        <span class="session-count">{{ item.messages >= 0 ? item.messages : '?' }}</span>
        <button
          v-if="item.name !== data?.current"
          type="button"
          class="del-btn"
          :disabled="loading"
          title="borrar"
          @click.stop="onDelete(item.name)"
        >×</button>
      </div>
      <div v-if="data && data.items.length === 0" class="placeholder mono">
        (sin sesiones)
      </div>
    </div>

    <div v-if="err" class="error mono">{{ err }}</div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 240px;
  flex-shrink: 0;
  background: var(--bg-elev);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  padding: 16px 12px;
  gap: 12px;
  overflow: hidden;
}
.brand {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 0 4px 8px;
  border-bottom: 1px solid var(--border);
}
.brand-name {
  font-size: 18px;
  font-weight: 700;
  color: var(--accent);
}
.brand-tag {
  font-size: 11px;
  color: var(--fg-dim);
}

.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 4px;
}
.section-label {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--fg-dim);
}
.icon-btn {
  background: transparent;
  color: var(--fg-dim);
  border: 1px solid var(--border);
  border-radius: 4px;
  width: 22px;
  height: 22px;
  font-size: 14px;
  cursor: pointer;
  padding: 0;
  line-height: 1;
}
.icon-btn:hover {
  background: var(--bg);
  color: var(--accent);
  border-color: var(--accent);
}

.new-form {
  display: flex;
  gap: 4px;
  padding: 0 4px;
}
.new-form input {
  flex: 1;
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.new-form input:focus { border-color: var(--accent); }
.new-form button {
  background: var(--accent);
  color: #0f172a;
  border: none;
  border-radius: 4px;
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}
.new-form button:disabled { opacity: 0.4; cursor: not-allowed; }

.session-list {
  flex: 1;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.session-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border-radius: 4px;
  font-size: 13px;
  cursor: pointer;
  position: relative;
}
.session-item:hover {
  background: rgba(255, 255, 255, 0.04);
}
.session-item.active {
  background: rgba(125, 211, 252, 0.1);
  color: var(--accent);
  cursor: default;
}
.session-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.session-count {
  font-size: 11px;
  color: var(--fg-dim);
  background: var(--bg);
  padding: 1px 6px;
  border-radius: 10px;
}
.del-btn {
  background: transparent;
  border: none;
  color: var(--fg-dim);
  cursor: pointer;
  padding: 0 4px;
  font-size: 14px;
  opacity: 0;
  transition: opacity 120ms, color 120ms;
}
.session-item:hover .del-btn { opacity: 1; }
.del-btn:hover { color: var(--error); }

.placeholder {
  padding: 8px 10px;
  font-size: 11px;
  color: var(--fg-dim);
  font-style: italic;
}
.error {
  margin-top: auto;
  padding: 6px 8px;
  font-size: 11px;
  color: var(--error);
  background: rgba(248, 113, 113, 0.08);
  border-radius: 4px;
}
</style>
