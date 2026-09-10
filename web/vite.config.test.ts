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
    for (const chunk of chunks) {
      const size = Buffer.byteLength(chunk.code)
      // 完整 Mermaid 解析器使用单独预算，其他模块继续遵守原来的限制。
      const budget = chunk === parser ? 750_000 : 500_000
      assert.ok(size <= budget, `${chunk.fileName}: ${size} bytes exceeds the ${budget / 1000} kB budget`)
    }
    const initialChunks = new Set<string>()
    const visit = (fileName: string) => {
      if (initialChunks.has(fileName)) return
      initialChunks.add(fileName)
      chunks.find(chunk => chunk.fileName === fileName)?.imports.forEach(visit)
    }
    chunks.filter(chunk => chunk.isEntry).forEach(chunk => visit(chunk.fileName))
    assert.ok(!initialChunks.has(parser.fileName), 'Mermaid parser must stay out of the initial payload')
    const renderer = chunks.find(chunk => chunk.isDynamicEntry && chunk.name === 'mermaid')
    assert.ok(renderer, 'Mermaid renderer must remain lazy-loaded')
    assert.ok(!initialChunks.has(renderer.fileName), 'Mermaid renderer must stay out of the initial payload')
    for (const page of ['SystemInfoPage', 'ProviderManagementPage', 'AccessLogPage', 'TokenUsagePage']) {
      assert.ok(chunks.some(chunk => chunk.isDynamicEntry && chunk.name === page), `${page} must remain lazy-loaded`)
    }
  }
})
