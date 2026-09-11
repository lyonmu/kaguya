#!/usr/bin/env node
// 从 material-icon-theme 官方 npm 包生成按需内嵌的文件图标数据。
// 用法：node web/scripts/generate-file-icons.mjs
// 只保留下方 ICONS 列出的图标，避免整套图标进入前端包。
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const VERSION = '5.38.1'
// 覆盖与 highlight.ts 一致的语言集，外加常见配置、文档与媒体文件。
const ICONS = [
  'file',
  'go', 'go-mod', 'typescript', 'react_ts', 'javascript', 'react', 'python', 'python-misc',
  'rust', 'java', 'kotlin', 'c', 'h', 'cpp', 'hpp', 'csharp', 'ruby', 'php', 'swift',
  'lua', 'perl', 'vue', 'svelte', 'html', 'css', 'sass', 'less', 'stylus', 'console',
  'json', 'yaml', 'toml', 'settings', 'xml', 'markdown', 'database', 'table', 'log', 'diff',
  'git', 'docker', 'makefile', 'cmake', 'lock', 'key', 'certificate', 'tune',
  'editorconfig', 'eslint', 'prettier', 'npm', 'nodejs', 'yarn', 'bun', 'tsconfig',
  'gemfile', 'poetry', 'hosts', 'nginx', 'vim',
  'license', 'readme', 'changelog', 'document', 'image', 'svg', 'pdf', 'zip', 'font',
  'audio', 'video', 'word', 'powerpoint', 'hex', 'exe', 'dll', 'todo',
]
const OUTPUT = new URL('../src/features/code/fileIconData.ts', import.meta.url)

// 同一个图标可能在页面里出现多次，给 defs 里的 id 加上图标前缀避免文档级冲突。
function namespaceIds(body, prefix) {
  const ids = new Set([...body.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]))
  let result = body
  for (const id of ids) {
    result = result
      .replaceAll(`id="${id}"`, `id="${prefix}-${id}"`)
      .replaceAll(`url(#${id})`, `url(#${prefix}-${id})`)
      .replaceAll(`href="#${id}"`, `href="#${prefix}-${id}"`)
  }
  return result
}

const response = await fetch(`https://registry.npmjs.org/material-icon-theme/${VERSION}`)
if (!response.ok) throw new Error(`fetch material-icon-theme metadata: ${response.status}`)
const { dist } = await response.json()
const workdir = mkdtempSync(join(tmpdir(), 'material-icons-'))
try {
  const tarball = join(workdir, 'package.tgz')
  const download = await fetch(dist.tarball)
  if (!download.ok) throw new Error(`download tarball: ${download.status}`)
  writeFileSync(tarball, Buffer.from(await download.arrayBuffer()))
  execFileSync('tar', ['-xzf', tarball, '-C', workdir])

  const manifest = JSON.parse(readFileSync(join(workdir, 'package/dist/material-icons.json'), 'utf8'))
  const license = readFileSync(join(workdir, 'package/LICENSE'), 'utf8')
  const known = new Set(ICONS)
  const data = {}
  for (const name of ICONS) {
    const svg = readFileSync(join(workdir, `package/icons/${name}.svg`), 'utf8')
    const match = svg.match(/<svg\b([^>]*)>([\s\S]*)<\/svg>/)
    if (!match) throw new Error(`parse svg: ${name}`)
    const viewBox = match[1].match(/viewBox="([^"]+)"/)?.[1].trim().split(/[\s,]+/).map(Number)
    const width = viewBox?.[2] || 16
    const height = viewBox?.[3] || 16
    const body = namespaceIds(match[2].trim(), `mit-${name}`)
    data[name] = { body, width, height }
  }

  const pick = mapping => Object.fromEntries(
    Object.entries(mapping)
      .filter(([, icon]) => known.has(icon))
      .sort(([a], [b]) => a.localeCompare(b)),
  )
  const extensions = pick(manifest.fileExtensions)
  const names = pick(manifest.fileNames)

  const emit = mapping => Object.entries(mapping)
    .map(([key, value]) => `  ${JSON.stringify(key)}: ${JSON.stringify(value)},`)
    .join('\n')
  const content = `// 该文件由 web/scripts/generate-file-icons.mjs 生成，请勿手动修改。
// 图标来自 Material Icon Theme v${VERSION}：
// https://github.com/material-extensions/vscode-material-icon-theme
//
${license.trim().split('\n').map(line => `// ${line}`.trimEnd()).join('\n')}
export interface FileIconData {
  body: string
  width: number
  height: number
}

export const FILE_ICON_DATA: Record<string, FileIconData> = {
${emit(data)}
}

// 完整文件名（小写）到图标名，优先于扩展名匹配。
export const FILE_ICON_BY_NAME: Record<string, string> = {
${emit(names)}
}

// 扩展名（小写，支持 ts.map 这类复合后缀）到图标名。
export const FILE_ICON_BY_EXTENSION: Record<string, string> = {
${emit(extensions)}
}
`
  writeFileSync(OUTPUT, content)
  const bytes = Buffer.byteLength(content)
  console.log(`generated ${ICONS.length} icons, ${Object.keys(names).length} names, ${Object.keys(extensions).length} extensions, ${(bytes / 1024).toFixed(1)} KiB`)
} finally {
  rmSync(workdir, { recursive: true, force: true })
}
