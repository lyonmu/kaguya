import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import css from 'highlight.js/lib/languages/css'
import diff from 'highlight.js/lib/languages/diff'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import go from 'highlight.js/lib/languages/go'
import ini from 'highlight.js/lib/languages/ini'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import kotlin from 'highlight.js/lib/languages/kotlin'
import less from 'highlight.js/lib/languages/less'
import lua from 'highlight.js/lib/languages/lua'
import makefile from 'highlight.js/lib/languages/makefile'
import markdown from 'highlight.js/lib/languages/markdown'
import perl from 'highlight.js/lib/languages/perl'
import php from 'highlight.js/lib/languages/php'
import python from 'highlight.js/lib/languages/python'
import ruby from 'highlight.js/lib/languages/ruby'
import rust from 'highlight.js/lib/languages/rust'
import scss from 'highlight.js/lib/languages/scss'
import sql from 'highlight.js/lib/languages/sql'
import stylus from 'highlight.js/lib/languages/stylus'
import swift from 'highlight.js/lib/languages/swift'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

// 语言注册表同时供聊天代码块、文件查看器与 diff 高亮使用。
// 扩展名别名是必须的：@git-diff-view 以文件扩展名作为语言名。
export const highlightLanguages: Record<string, Parameters<typeof hljs.registerLanguage>[1]> = {
  bash, c, cpp, csharp, css, diff, dockerfile, go, ini, java, javascript, json,
  kotlin, less, lua, makefile, markdown, perl, php, python, ruby, rust, scss,
  sql, stylus, swift, typescript, xml, yaml,
  js: javascript, jsx: javascript, mjs: javascript, cjs: javascript,
  ts: typescript, tsx: typescript,
  py: python, rb: ruby, yml: yaml, sh: bash, zsh: bash, bashrc: bash,
  h: c, cc: cpp, hpp: cpp, htm: xml, html: xml, svg: xml, vue: xml,
  md: markdown, patch: diff, toml: ini, cfg: ini, conf: ini,
  Dockerfile: dockerfile, Makefile: makefile, Gnumakefile: makefile,
}
for (const [name, language] of Object.entries(highlightLanguages)) hljs.registerLanguage(name, language)

// 高亮整文件前的大小上限，超出时降级为纯文本以保证滚动流畅。
export const HIGHLIGHT_MAX_CHARS = 200_000

const LANGUAGE_BY_EXTENSION: Record<string, string> = {
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  go: 'go', py: 'python', rb: 'ruby', php: 'php', java: 'java', kt: 'kotlin',
  kts: 'kotlin', swift: 'swift', rs: 'rust', lua: 'lua', pl: 'perl',
  c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp', cs: 'csharp',
  sh: 'bash', bash: 'bash', zsh: 'bash', bashrc: 'bash',
  sql: 'sql', diff: 'diff', patch: 'diff',
  json: 'json', jsonc: 'json',
  yaml: 'yaml', yml: 'yaml', toml: 'ini', ini: 'ini', cfg: 'ini', conf: 'ini',
  md: 'markdown', markdown: 'markdown',
  html: 'xml', htm: 'xml', xml: 'xml', vue: 'xml', svg: 'xml',
  css: 'css', scss: 'scss', less: 'less',
  dockerfile: 'dockerfile', makefile: 'makefile',
}

export function langFromPath(path: string): string {
  const name = path.slice(path.lastIndexOf('/') + 1).toLowerCase()
  if (name === 'dockerfile') return 'dockerfile'
  if (name === 'makefile' || name === 'gnumakefile') return 'makefile'
  const dot = name.lastIndexOf('.')
  if (dot < 0) return ''
  return LANGUAGE_BY_EXTENSION[name.slice(dot + 1)] ?? ''
}

export function highlightCode(code: string, language: string): string | undefined {
  if (!language || code.length > HIGHLIGHT_MAX_CHARS || !hljs.getLanguage(language)) return
  return hljs.highlight(code, { language, ignoreIllegals: true }).value
}

const ESCAPE: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;' }
export function escapeHtml(text: string): string {
  return text.replace(/[&<>]/g, character => ESCAPE[character])
}

// 按行拆分 highlight.js 的输出：跨行的 span 在换行处闭合，并在下一行原样重开，
// 使每行都是可独立渲染的 HTML 片段。
export function splitHighlightedLines(html: string): string[] {
  const lines: string[] = []
  const open: string[] = []
  let current = ''
  let index = 0
  const appendText = (text: string) => {
    let rest = text
    for (;;) {
      const newline = rest.indexOf('\n')
      if (newline < 0) {
        current += rest
        return
      }
      current += rest.slice(0, newline)
      lines.push(current + '</span>'.repeat(open.length))
      current = open.join('')
      rest = rest.slice(newline + 1)
    }
  }
  while (index < html.length) {
    const next = html.indexOf('<', index)
    if (next < 0) {
      appendText(html.slice(index))
      break
    }
    appendText(html.slice(index, next))
    const end = html.indexOf('>', next)
    if (end < 0) {
      appendText(html.slice(next))
      break
    }
    const tag = html.slice(next, end + 1)
    if (tag.startsWith('</')) open.pop()
    else open.push(tag)
    current += tag
    index = end + 1
  }
  lines.push(current + '</span>'.repeat(open.length))
  return lines
}

export function plainLines(content: string): string[] {
  return content.split('\n').map(escapeHtml)
}
