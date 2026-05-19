<script setup lang="ts">
import { onMounted } from 'vue'
import Sidebar from './components/Sidebar.vue'
import ChatLog from './components/ChatLog.vue'
import ChatInput from './components/ChatInput.vue'
import { useChat } from './stores/chat'

const { messages, sending, send, cancel, loadHistory } = useChat()

// Al montar, reconstruimos la conversación desde el backend para que
// sobreviva al refresh de la página.
onMounted(loadHistory)

// Al cambiar de sesión, recargamos el history de la nueva.
async function onSwitched(_name: string, _count: number) {
  await loadHistory()
}
</script>

<template>
  <div class="layout">
    <Sidebar :busy="sending" @switched="onSwitched" />
    <main class="main">
      <ChatLog :messages="messages" />
      <ChatInput :sending="sending" @submit="send" @cancel="cancel" />
    </main>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  height: 100%;
}
.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
</style>
