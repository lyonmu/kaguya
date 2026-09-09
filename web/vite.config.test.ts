/// <reference types="node" />
import { it } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'

it('keeps production chunks below 500 kB and preserves lazy system pages', async () => {
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
    for (const chunk of chunks) {
      const size = Buffer.byteLength(chunk.code)
      assert.ok(size <= 500_000, `${chunk.fileName}: ${size} bytes exceeds the 500 kB budget`)
    }
    for (const page of ['SystemInfoPage', 'ProviderManagementPage', 'AccessLogPage']) {
      assert.ok(chunks.some(chunk => chunk.isDynamicEntry && chunk.name === page), `${page} must remain lazy-loaded`)
    }
  }
})
