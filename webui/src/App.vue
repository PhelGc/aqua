<script setup lang="ts">
// Placeholder: el chat real llega en el próximo commit.
</script>

<template>
  <main class="shell">
    <header>aqua · web</header>
    <section class="body">
      <p>scaffold listo · próximo paso: cliente API y chat funcional.</p>
      <p class="mono dim">vite dev en :5173 · proxy a aqua en :7777</p>
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
}
.dim {
  color: var(--fg-dim);
}
</style>
