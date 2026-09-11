import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { Agent } from 'node:https'
import { fileURLToPath } from 'node:url'
import { developmentCA } from './dev-ca.ts'

// @git-diff-view/core 静态引入 lowlight 的全量语言集（约 1MB）。把库内部的
// `lowlight` 解析到精选语言集的替代模块：highlighter 接口不变，但只打包常用语言，
// 且不会把高亮语言包带进聊天首屏。
function lowlightSubset(): Plugin {
  const subset = fileURLToPath(new URL('./src/features/code/lowlightSubset.ts', import.meta.url))
  return {
    name: 'kaguya:lowlight-subset',
    enforce: 'pre',
    resolveId(source, importer) {
      if (source === 'lowlight' && importer?.includes('@git-diff-view')) return subset
      return null
    },
  }
}

export default defineConfig(({ command }) => ({
  plugins: [react(), tailwindcss(), lowlightSubset()],
  // 让开发服务器也使用与生产一致的精选语言集。
  optimizeDeps: { exclude: ['@git-diff-view/lowlight', 'lowlight'] },
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              // 高亮语言集与 lowlight 适配层被 diff 引擎和文件查看器共享；
              // 单独成包以避免 vendor-diff-view 反向依赖应用入口 chunk 形成循环。
              name: 'code-highlight',
              test: /src[\\/]features[\\/]code[\\/](?:highlight|lowlightSubset)\.ts/,
              includeDependenciesRecursively: false,
            },
            {
              // diff 引擎及其运行时依赖：包含辅助包以避免共享模块
              // 回落到应用入口 chunk 造成反向依赖。
              name: 'vendor-diff-view',
              test: /[\\/]node_modules[\\/](?:@git-diff-view|@vue|reactivity-store|use-sync-external-store|fast-diff)[\\/]/,
              includeDependenciesRecursively: false,
            },
            {
              // 高亮引擎被聊天气泡与 diff 视图共用，独立缓存且不递归带入语言包以外的依赖。
              name: 'vendor-highlight',
              test: /[\\/]node_modules[\\/](?:highlight\.js|lowlight)[\\/]/,
              includeDependenciesRecursively: false,
            },
            {
              // Ant Design X Mermaid 的代码视图携带完整 Prism 语法集合；单独缓存，
              // 避免它进入聊天首屏或与 Mermaid 渲染核心合并。
              name: 'vendor-antdx-highlighter',
              test: /[\\/]node_modules[\\/](?:react-syntax-highlighter|refractor)[\\/]/,
              includeDependenciesRecursively: false,
            },
            {
              // Mermaid's bundled parser is larger than the general chunk budget;
              // isolate it so it stays out of the initial chat payload.
              name: 'vendor-mermaid-parser',
              test: /[\\/]node_modules[\\/]@mermaid-js[\\/]parser[\\/]/,
              includeDependenciesRecursively: false,
            },
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
