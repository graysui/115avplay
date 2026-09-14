import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: false,
  },
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://127.0.0.1:8096',
      '/emby': 'http://127.0.0.1:8096',
    },
  },
})
