import { describe, it as test } from 'node:test'
import assert from 'node:assert/strict'
import { parseArgs, parseStringMap, toMCPConfig, toMCPForm } from './form'

describe('MCP configuration form', () => {
  test('preserves credentials and arguments across editing', () => {
    const config = { name: 'local', transport: 'stdio' as const, command: '/bin/server', args: ['--path', '/path with spaces'], env: { KEY: 'secret=with spaces' }, working_directory: '/tmp', url: '', headers: {}, timeout_seconds: 60 }
    assert.deepEqual(toMCPConfig(toMCPForm(config)), config)
  })
  test('clears incompatible fields when changing transport', () => {
    const config = toMCPConfig({ name: ' remote ', transport: 'sse', command: 'old', env_json: '{invalid', args_json: 'also invalid', headers_json: '{"Authorization":"Bearer secret"}', url: ' https://example.com/mcp ', timeout_seconds: 30 })
    assert.deepEqual(config, { name: 'remote', transport: 'sse', command: '', args: [], env: {}, working_directory: '', url: 'https://example.com/mcp', headers: { Authorization: 'Bearer secret' }, timeout_seconds: 30 })
  })
  test('rejects invalid JSON, mixed argument types and non-string secrets', () => {
    for (const value of ['{', '{}', '["ok",1]', 'null']) assert.throws(() => parseArgs(value))
    for (const value of ['{', '[]', 'null', '{"PORT":123}']) assert.throws(() => parseStringMap(value, '环境变量'))
    assert.deepEqual(parseArgs(''), [])
    assert.deepEqual(parseStringMap('', '环境变量'), {})
  })
})
