import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../webassets/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules/echarts') || id.includes('node_modules/zrender')) return 'echarts'
          if (id.includes('node_modules/vuetify')) return 'vuetify'
          if (id.includes('node_modules/vue') || id.includes('node_modules/@vue')) return 'vue'
          return undefined
        }
      }
    }
  },
  server: {
    proxy: { '/api': 'http://127.0.0.1:28637', '/healthz': 'http://127.0.0.1:28637' }
  }
})
