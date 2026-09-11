// @git-diff-view/core 会静态引入 lowlight 的全量语言集（约 1MB）。
// 构建时通过 vite.config.ts 的 lowlightSubset 插件把库内部的 `lowlight` 导入
// 指向本模块：接口保持不变，只把 `all` 换成与聊天/文件查看器一致的精选语言集。
import { createLowlight } from 'lowlight'
import { highlightLanguages } from './highlight'

export { createLowlight }
export const all = highlightLanguages
