import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Proxy a aqua que corre en :7777. Las llamadas del frontend a /command,
// /events, /api/* y /reports/ se reenvían sin necesidad de CORS.
// SSE necesita changeOrigin y configure para no bufferear la respuesta.
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/command': {
        target: 'http://127.0.0.1:7777',
        changeOrigin: true,
        // SSE necesita que el proxy no buferee y no fuerce timeout corto.
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Accept', 'text/event-stream')
          })
        },
      },
      '/events': {
        target: 'http://127.0.0.1:7777',
        changeOrigin: true,
      },
      '/api': { target: 'http://127.0.0.1:7777', changeOrigin: true },
      '/reports': { target: 'http://127.0.0.1:7777', changeOrigin: true },
      '/upload': { target: 'http://127.0.0.1:7777', changeOrigin: true },
    },
  },
})
