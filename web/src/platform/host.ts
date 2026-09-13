/**
 * Desktop 宿主适配层。
 *
 * 页面由 Wails 原生 scheme（wails://localhost）加载时，系统操作走宿主提供的
 * 窄接口；普通浏览器继续使用 Web API。这里不依赖 Wails runtime，
 * 同一份前端产物在两种环境都能运行。
 */

export const DESKTOP_PROTOCOL = 'wails:'
export const DESKTOP_HOSTNAME = 'localhost'
export const API_PREFIX_PARAM = 'api-prefix'
export const NATIVE_ROUTE_PREFIX = '/__desktop'

export interface HostLocation {
  protocol: string
  hostname: string
  hash: string
}

export type PrefixResult = { ok: true; prefix: string } | { ok: false; error: string }

const PREFIX_PATTERN = /^\/[A-Za-z0-9._~\-/]+$/
const RESERVED_PATHS = ['/wails', NATIVE_ROUTE_PREFIX, '/assets', '/kaguya-favicon.webp']

export function isDesktopLocation(location: HostLocation): boolean {
  return location.protocol === DESKTOP_PROTOCOL && location.hostname === DESKTOP_HOSTNAME
}

export function isDesktop(): boolean {
  return typeof globalThis.location !== 'undefined' && isDesktopLocation(globalThis.location)
}

function overlaps(a: string, b: string): boolean {
  return a === b || a.startsWith(`${b}/`) || b.startsWith(`${a}/`)
}

/**
 * 解析宿主通过 fragment 注入的 API 前缀。只接受规范本地路径，
 * 非法配置返回错误而不是退回外部地址。
 */
export function parseDesktopApiPrefix(hash: string): PrefixResult {
  const rawHash = hash.startsWith('#') ? hash.slice(1) : hash
  const prefix = new URLSearchParams(rawHash).get(API_PREFIX_PARAM)
  if (prefix === null || prefix === '') {
    return { ok: false, error: '桌面配置缺少 API 前缀' }
  }
  if (!PREFIX_PATTERN.test(prefix) || prefix.includes('//')) {
    return { ok: false, error: '桌面配置的 API 前缀不是合法的本地路径' }
  }
  if (prefix === '/' || prefix.endsWith('/')) {
    return { ok: false, error: '桌面配置的 API 前缀不能指向根路径或以斜杠结尾' }
  }
  const segments = prefix.split('/').slice(1)
  if (segments.some(segment => segment === '' || segment === '.' || segment === '..')) {
    return { ok: false, error: '桌面配置的 API 前缀包含越级路径' }
  }
  if (RESERVED_PATHS.some(reserved => overlaps(prefix, reserved))) {
    return { ok: false, error: '桌面配置的 API 前缀与保留路径冲突' }
  }
  return { ok: true, prefix }
}

/** desktopApiBase 返回 Desktop 模式下的 API base；Web 模式不适用。 */
export function desktopApiBase(): PrefixResult {
  if (!isDesktop()) {
    return { ok: false, error: '当前页面不是 Kaguya Desktop 窗口' }
  }
  return parseDesktopApiPrefix(globalThis.location.hash)
}

async function postNative(path: string, body: unknown): Promise<void> {
  const response = await fetch(`${NATIVE_ROUTE_PREFIX}/${path}`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  })
  if (!response.ok) throw new Error('系统操作失败')
}

/** copyText 在 Desktop 调用宿主剪贴板，Web 继续使用浏览器剪贴板。 */
export async function copyText(text: string): Promise<void> {
  if (!isDesktop()) {
    await navigator.clipboard.writeText(text)
    return
  }
  await postNative('clipboard', { text })
}

/** openExternal 在 Desktop 交给系统浏览器打开，Web 使用新标签页。 */
export async function openExternal(url: string): Promise<void> {
  if (!isDesktop()) {
    globalThis.open(url, '_blank', 'noopener,noreferrer')
    return
  }
  await postNative('open-external', { url })
}
