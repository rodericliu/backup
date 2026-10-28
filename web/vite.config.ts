import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// In development the API is proxied to a local backup-server (make run-dev).
const backend = process.env.BACKUP_SERVER ?? 'https://127.0.0.1:8443'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': new URL('./src', import.meta.url).pathname } },
  server: {
    proxy: { '/api': { target: backend, changeOrigin: false, secure: false } },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
})
