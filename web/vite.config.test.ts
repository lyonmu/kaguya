/// <reference types="node" />
import { it } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'

it('enforces chunk budgets and keeps Mermaid and system pages lazy', async () => {
  // 使用真实生产配置，在内存中构建，避免测试覆盖 dist 或嵌入后端的前端文件。
  const result = await build({
    root: fileURLToPath(new URL('.', import.meta.url)),
    configFile: fileURLToPath(new URL('./vite.config.ts', import.meta.url)),
    logLevel: 'silent',
    build: { write: false },
  })
  const outputs = Array.isArray(result) ? result : [result]
  for (const output of outputs) {
    assert.ok('output' in output)
    const chunks = output.output.filter(item => item.type === 'chunk')
    assert.ok(chunks.some(chunk => chunk.fileName.includes('vendor-react-')))
    const parser = chunks.find(chunk => chunk.name === 'vendor-mermaid-parser')
    assert.ok(parser, 'Mermaid parser must have its own chunk')
    const highlighter = chunks.find(chunk => chunk.name === 'vendor-antdx-highlighter')
    assert.ok(highlighter, 'Ant Design X code highlighter must have its own chunk')
    for (const chunk of chunks) {
      const size = Buffer.byteLength(chunk.code)
      // Mermaid 解析器与 Ant Design X 的完整 Prism 集合只在图表交互时加载。
      const budget = chunk === parser || chunk === highlighter ? 750_000 : 500_000
      assert.ok(size <= budget, `${chunk.fileName}: ${size} bytes exceeds the ${budget / 1000} kB budget`)
    }
    const initialChunks = new Set<string>()
    const visit = (fileName: string) => {
      if (initialChunks.has(fileName)) return
      initialChunks.add(fileName)
      chunks.find(chunk => chunk.fileName === fileName)?.imports.forEach(visit)
    }
    chunks.filter(chunk => chunk.isEntry).forEach(chunk => visit(chunk.fileName))
    // 静态 chunk 循环会让模块初始化顺序错乱（典型症状是运行时 “x is not a function”），
    // 动态导入属于异步边界，不计入循环。
    const owners = new Map(chunks.map(chunk => [chunk.fileName, chunk]))
    const visiting = new Set<string>()
    const settled = new Set<string>()
    const cycle: string[] = []
    const detect = (fileName: string, chain: string[]): boolean => {
      if (visiting.has(fileName)) {
        cycle.push(...chain.slice(chain.indexOf(fileName)), fileName)
        return true
      }
      if (settled.has(fileName) || cycle.length) return cycle.length > 0
      visiting.add(fileName)
      for (const next of owners.get(fileName)?.imports ?? []) {
        if (!owners.has(next)) continue
        if (detect(next, [...chain, fileName])) break
      }
      visiting.delete(fileName)
      settled.add(fileName)
      return cycle.length > 0
    }
    for (const chunk of chunks) detect(chunk.fileName, [])
    assert.equal(cycle.length, 0, `chunk import cycle detected: ${cycle.join(' -> ')}`)
    assert.ok(!initialChunks.has(parser.fileName), 'Mermaid parser must stay out of the initial payload')
    assert.ok(!initialChunks.has(highlighter.fileName), 'Ant Design X code highlighter must stay out of the initial payload')
    const renderer = chunks.find(chunk => chunk.isDynamicEntry && chunk.name === 'mermaid')
    assert.ok(renderer, 'Mermaid renderer must remain lazy-loaded')
    assert.ok(!initialChunks.has(renderer.fileName), 'Mermaid renderer must stay out of the initial payload')
    for (const name of ['vendor-highlight', 'vendor-diff-view', 'CodeBrowserDrawer']) {
      const chunk = chunks.find(item => item.name === name)
      assert.ok(chunk, `${name} must exist`)
      assert.ok(!initialChunks.has(chunk.fileName), `${name} must stay out of the initial payload`)
    }
    for (const page of ['SystemInfoPage', 'ProviderManagementPage', 'AccessLogPage', 'TokenUsagePage']) {
      assert.ok(chunks.some(chunk => chunk.isDynamicEntry && chunk.name === page), `${page} must remain lazy-loaded`)
    }
  }
})
