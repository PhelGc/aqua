<script setup lang="ts">
// Smoke test del cliente API: pedimos el state al backend y lo mostramos.
// Si vemos el JSON, el proxy de Vite y el cliente funcionan.
// El chat real llega en el próximo commit.
import { ref, onMounted } from 'vue'
import { fetchState } from './api'
import type { ApiState } from './types'

const state = ref<ApiState | null>(null)
const err = ref<string | null>(null)

onMounted(async () => {
  try {
    state.value = await fetchState()
  } catch (e) {
    err.value = (e as Error).message
  }
})
</script>

<template>
  <main class="shell">
    <header>aqua · web</header>
    <section class="body">
      <p v-if="state" class="mono">
        sesión: <strong>{{ state.session }}</strong> ·
        mensajes: <strong>{{ state.messages }}</strong> ·
        busy: <strong>{{ state.busy }}</strong>
      </p>
      <p v-else-if="err" class="mono error">backend no responde: {{ err }}</p>
      <p v-else class="dim mono">consultando /api/state…</p>
      <p class="dim mono small">próximo paso: layout chat + input.</p>
    </section>
  </main>
</template>

<style scoped>
.shell {
  display: flex;
  flex-direction: column;
  height: 100%;
}
header {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  font-weight: 600;
  color: var(--accent);
}
.body {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dim { color: var(--fg-dim); }
.error { color: var(--error); }
.small { font-size: 12px; }
</style>
