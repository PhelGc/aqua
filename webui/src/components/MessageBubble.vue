<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ChatMessage } from '../types'

const props = defineProps<{ message: ChatMessage }>()

// Reasoning colapsado por default siempre. Si querés ver el razonamiento
// en vivo o consultarlo después, clickeás el summary para expandirlo.
const reasoningOpen = ref(false)
const hasReasoning = computed(() => !!props.message.reasoning)
</script>

<template>
  <!-- TOOL: badge inline entre mensajes, sin globo. -->
  <div v-if="message.role === 'tool'" class="tool">
    <span class="tool-icon">⚡</span>
    <span class="tool-name mono">{{ message.content }}</span>
  </div>

  <!-- SYSTEM: línea sutil tipo nota. -->
  <div v-else-if="message.role === 'system'" class="system mono">
    · {{ message.content }}
  </div>

  <!-- ERROR: similar a system pero rojo. -->
  <div v-else-if="message.role === 'error'" class="error mono">
    ✗ {{ message.content }}
  </div>

  <!-- USER / ASSISTANT: bubble con autor + contenido. -->
  <div v-else :class="['bubble', message.role]">
    <div class="author">
      {{ message.role === 'user' ? 'you' : 'aqua' }}
      <span v-if="message.streaming" class="dot" />
    </div>

    <!-- Reasoning colapsable encima del content. Siempre cerrado por
         default; el usuario lo expande con click si lo quiere ver. -->
    <details
      v-if="hasReasoning"
      class="thinking"
      :open="reasoningOpen"
      @toggle="reasoningOpen = ($event.target as HTMLDetailsElement).open"
    >
      <summary>
        {{ message.streaming && !message.content ? '· pensando…' : '» thinking' }}
      </summary>
      <div class="thinking-body mono">{{ message.reasoning }}</div>
    </details>

    <div v-if="message.content" class="content">{{ message.content }}</div>

    <!-- Attachments del usuario como chips. El contenido extraído fue al
         LLM en el prompt pero NO se muestra en la UI para no inundar el
         bubble con tablas/texto largo. -->
    <div v-if="message.attachments && message.attachments.length" class="attachments">
      <span
        v-for="att in message.attachments"
        :key="att.id"
        class="att-chip"
        :title="`${att.name} · ${humanSize(att.size)}`"
      >
        <span class="att-icon">{{ kindIcon(att.kind) }}</span>
        <span class="att-name">{{ att.name }}</span>
        <span class="att-size">{{ humanSize(att.size) }}</span>
      </span>
    </div>

    <div v-if="message.artifact" class="artifact">
      <a :href="`/reports/${encodeURIComponent(artifactName(message.artifact))}`" target="_blank">
        📄 {{ artifactName(message.artifact) }}
      </a>
    </div>
  </div>
</template>

<script lang="ts">
// Helper fuera del setup para que el template lo encuentre vía el <script>
// secundario. Saca el nombre del archivo del path absoluto que devuelve
// el backend (writeReport).
export function artifactName(path: string): string {
  const idx = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return idx >= 0 ? path.slice(idx + 1) : path
}

export function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export function kindIcon(kind: string): string {
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

<style scoped>
.bubble {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border-radius: 8px;
  max-width: 100%;
  word-wrap: break-word;
}
.bubble.user {
  background: rgba(167, 139, 250, 0.08);
  border-left: 2px solid var(--user);
}
.bubble.assistant {
  background: rgba(125, 211, 252, 0.05);
  border-left: 2px solid var(--accent);
}
.author {
  font-weight: 600;
  font-size: 12px;
  text-transform: lowercase;
  color: var(--fg-dim);
  letter-spacing: 0.4px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.bubble.user .author { color: var(--user); }
.bubble.assistant .author { color: var(--accent); }

.dot {
  width: 6px;
  height: 6px;
  background: var(--accent);
  border-radius: 50%;
  animation: pulse 1.2s ease-in-out infinite;
}
@keyframes pulse {
  0%, 100% { opacity: 0.3; }
  50% { opacity: 1; }
}

.content {
  white-space: pre-wrap;
  font-size: 14px;
  line-height: 1.55;
}

.thinking {
  border-left: 2px solid var(--border);
  padding-left: 10px;
  color: var(--fg-dim);
  font-size: 12px;
}
.thinking summary {
  cursor: pointer;
  user-select: none;
  font-style: italic;
}
.thinking-body {
  margin-top: 6px;
  white-space: pre-wrap;
  font-size: 12px;
  line-height: 1.5;
  opacity: 0.85;
}

.tool {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: rgba(251, 191, 36, 0.1);
  border: 1px solid rgba(251, 191, 36, 0.3);
  border-radius: 999px;
  color: var(--tool);
  font-size: 12px;
  align-self: flex-start;
}
.tool-name {
  font-size: 12px;
}

.system {
  color: var(--fg-dim);
  font-size: 12px;
  padding: 4px 0;
}
.error {
  color: var(--error);
  font-size: 13px;
  padding: 4px 0;
}

.attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}
.att-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  max-width: 280px;
}
.att-icon { font-size: 14px; }
.att-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.att-size {
  color: var(--fg-dim);
  font-size: 11px;
}

.artifact {
  margin-top: 4px;
  font-size: 12px;
}
.artifact a {
  color: var(--accent);
  text-decoration: none;
}
.artifact a:hover {
  text-decoration: underline;
}
</style>
