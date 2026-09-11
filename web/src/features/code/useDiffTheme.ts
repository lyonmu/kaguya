import { useEffect, useState } from 'react'

// 与 colorMode.ts 维护的 data-theme 保持一致，供第三方 diff 视图切换明暗主题。
export function useDiffTheme(): 'light' | 'dark' {
  const [mode, setMode] = useState<'light' | 'dark'>(() => (typeof document !== 'undefined' && document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'))
  useEffect(() => {
    const observer = new MutationObserver(() => setMode(document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])
  return mode
}
