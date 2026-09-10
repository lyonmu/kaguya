import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { developmentCA } from './dev-ca.ts'
import { Agent } from 'node:https'

export default defineConfig(({ command }) => ({
  plugins: [react(), tailwindcss()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              // Cache the chat runtime separately from application code.
              name: 'vendor-assistant',
              test: /[\\/]node_modules[\\/]@assistant-ui[\\/]/,
            },
            {
              // 图表渲染引擎单独缓存，仍仅由懒加载用量页面引入。
              name: 'vendor-zrender',
              test: /[\\/]node_modules[\\/]zrender[\\/]/,
            },
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
        target: 'https://localhost:9024',
        agent: command === 'serve' ? new Agent({ minVersion: 'TLSv1.3', ca: developmentCA() }) : undefined,
        // Preserve the browser's matching Host/Origin through the local proxy.
        changeOrigin: false,
      },
    },
  },
}))
