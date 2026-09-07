import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/kaguya/api': {
        target: 'http://localhost:9024',
        changeOrigin: true,
      },
    },
  },
})
