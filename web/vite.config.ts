import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              // 独立缓存 React 运行时，避免与 Ant Design 合并成超大的共享包。
              // 其余依赖保留自动拆分，避免将懒加载页面依赖提前打入首屏。
              name: 'vendor-react',
              test: /[\\/]node_modules[\\/](?:react|react-dom|scheduler)[\\/]/,
            },
          ],
        },
      },
    },
  },
  server: {
    proxy: {
      '/kaguya/api': {
        target: 'http://localhost:9024',
        changeOrigin: true,
      },
    },
  },
})
