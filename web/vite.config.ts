import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath } from 'node:url'

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

// @ant-design/x 的代码高亮默认使用 PrismLight 并按语言动态注册，只有
// prismLightMode=false 才会走全量 Prism；而全量入口会引入 refractor/all
// 的全部语法（约 600KB）。这里把裸包名重定向到 PrismLight，保留导出形状，
// 同时避免整包语法进入构建；按语言动态导入的子路径不受影响。
function prismLightSubset(): Plugin {
  const virtualId = '\0kaguya:react-syntax-highlighter'
  return {
    name: 'kaguya:prism-light-subset',
    enforce: 'pre',
    resolveId(source) {
      if (source === 'react-syntax-highlighter') return virtualId
      return null
    },
    load(id) {
      if (id !== virtualId) return null
      return [
        "import PrismLight from 'react-syntax-highlighter/dist/esm/prism-light'",
        'export { PrismLight }',
        'export const Prism = PrismLight',
      ].join('\n')
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), lowlightSubset(), prismLightSubset()],
  // 让开发服务器也使用与生产一致的精选语言集与 PrismLight。
  optimizeDeps: { exclude: ['@git-diff-view/lowlight', 'lowlight', 'react-syntax-highlighter'] },
  build: {
    // Mermaid 解析器核心是单个预打包模块（打包后约 680KB），无法再拆分；
    // 它只由新版 Mermaid 图表按需加载，因此放宽单块体积告警阈值。
    chunkSizeWarningLimit: 700,
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
              // Ant Design X Mermaid 代码视图的高亮器与样式；语言按需注册，
              // 单独缓存以免进入聊天首屏。虚拟模块与高亮器同块，避免产生导入环。
              name: 'vendor-antdx-highlighter',
              test: /[\\/]node_modules[\\/](?:react-syntax-highlighter|refractor)[\\/]|kaguya:react-syntax-highlighter/,
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
              // 图表引擎（ECharts 及其 zrender 渲染器）只由懒加载的用量页面引入。
              // 它是仓库中最大的第三方图形库，独立成包既避免页面 chunk 被撑大，
              // 也让页面代码变化不会使整个图形引擎缓存失效。
              // 注意：该包体积固定超过通用预算，测试中单独设阈值。
              name: 'vendor-echarts',
              test: /[\\/]node_modules[\\/](?:echarts|zrender)[\\/]/,
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
        target: 'http://127.0.0.1:9024',
        // Preserve the browser's matching Host/Origin through the local proxy.
        changeOrigin: false,
      },
    },
  },
})
