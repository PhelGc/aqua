<script setup lang="ts">
// Placeholder. Cuando agreguemos sesiones, esto va a listar/cambiar/crear.
import { onMounted, ref } from 'vue'
import { fetchState } from '../api'

const session = ref('default')
const messages = ref(0)

onMounted(async () => {
  try {
    const s = await fetchState()
    session.value = s.session
    messages.value = s.messages
  } catch {
    /* el header del chat ya avisa si hay errores */
  }
})
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <span class="brand-name">aqua</span>
      <span class="brand-tag mono">web</span>
    </div>
    <div class="section-label">SESIÓN ACTUAL</div>
    <div class="session-item active">
      <span class="session-name mono">{{ session }}</span>
      <span class="session-count">{{ messages }}</span>
    </div>
    <div class="placeholder mono">
      (gestión de sesiones próximamente)
    </div>
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
.section-label {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--fg-dim);
  padding: 0 4px;
}
.session-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 13px;
  background: transparent;
}
.session-item.active {
  background: rgba(125, 211, 252, 0.1);
  color: var(--accent);
}
.session-name { font-weight: 500; }
.session-count {
  font-size: 11px;
  color: var(--fg-dim);
  background: var(--bg);
  padding: 2px 6px;
  border-radius: 10px;
}
.placeholder {
  margin-top: auto;
  padding: 8px 4px;
  font-size: 11px;
  color: var(--fg-dim);
  font-style: italic;
}
</style>
