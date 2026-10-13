import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const backend = process.env.ORBIT_BACKEND ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: backend, ws: true },
      '/git': backend,
    },
  },
})
