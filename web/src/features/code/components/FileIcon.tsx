import { fileIconForPath } from '../fileIcons'

// 渲染内嵌的 Material Icon Theme 图标数据，未知类型回退到通用文件图标。
export function FileIcon({ path, size = 16 }: { path: string; size?: number }) {
  const icon = fileIconForPath(path)
  return (
    <svg
      className="code-file-icon"
      width={size}
      height={size}
      viewBox={icon.viewBox}
      aria-hidden="true"
      focusable="false"
      dangerouslySetInnerHTML={{ __html: icon.body }}
    />
  )
}
