<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import MessageBubble from './MessageBubble.vue'
import type { ChatMessage } from '../types'

const props = defineProps<{ messages: ChatMessage[] }>()
const scroller = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)

// Si el usuario scrolleó hacia arriba, dejamos de auto-scrollear.
function onScroll() {
  const el = scroller.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
  stickToBottom.value = nearBottom
}

// Cada vez que cambia la lista (o el contenido del último), auto-scroll
// si el usuario está pegado al fondo.
watch(
  () => [props.messages.length, props.messages[props.messages.length - 1]?.content],
  async () => {
    if (!stickToBottom.value) return
    await nextTick()
    const el = scroller.value
    if (el) el.scrollTop = el.scrollHeight
  },
  { flush: 'post' },
)
</script>

<template>
  <div ref="scroller" class="log" @scroll="onScroll">
    <div v-if="messages.length === 0" class="empty mono">
      escribí algo abajo para empezar.
    </div>
    <MessageBubble
      v-for="m in messages"
      :key="m.id"
      :message="m"
    />
  </div>
</template>

<style scoped>
.log {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  scroll-behavior: smooth;
}
.empty {
  color: var(--fg-dim);
  text-align: center;
  padding: 40px 0;
}
.log::-webkit-scrollbar { width: 8px; }
.log::-webkit-scrollbar-track { background: transparent; }
.log::-webkit-scrollbar-thumb {
  background: var(--border);
  border-radius: 4px;
}
.log::-webkit-scrollbar-thumb:hover { background: var(--fg-dim); }
</style>
