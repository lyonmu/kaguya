import { FILE_ICON_BY_EXTENSION, FILE_ICON_BY_NAME, FILE_ICON_DATA, type FileIconData } from './fileIconData'

const FALLBACK_ICON = 'file'

// 按 VS Code 文件图标的规则解析：完整文件名（含点文件）优先，再按最长后缀匹配扩展名。
export function iconNameForPath(path: string): string {
  const name = path.slice(path.lastIndexOf('/') + 1).toLowerCase()
  const exact = FILE_ICON_BY_NAME[name]
  if (exact) return exact
  const parts = name.split('.')
  for (let start = 1; start < parts.length; start++) {
    const matched = FILE_ICON_BY_EXTENSION[parts.slice(start).join('.')]
    if (matched) return matched
  }
  return FALLBACK_ICON
}

export function fileIconForPath(path: string): FileIconData {
  return FILE_ICON_DATA[iconNameForPath(path)] ?? FILE_ICON_DATA[FALLBACK_ICON]
}
