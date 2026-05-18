<script setup lang="ts">
import Sidebar from './components/Sidebar.vue'
import ChatLog from './components/ChatLog.vue'
import ChatInput from './components/ChatInput.vue'
import { useChat } from './stores/chat'

const { messages, sending, send, cancel, clear, pushSystem } = useChat()

function onSwitched(name: string, count: number) {
  clear()
  pushSystem(`sesión: ${name} · ${count} mensajes en disco`)
}
</script>

<template>
  <div class="layout">
    <Sidebar @switched="onSwitched" />
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
