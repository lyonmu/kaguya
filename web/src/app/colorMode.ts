import { useLayoutEffect, useState } from 'react'
import { applyLunarPalette, lunarPalettes } from './lunar'

export type ColorMode = 'light' | 'dark'

const STORAGE_KEY = 'kaguya-color-mode'

function getSystemColorMode(): ColorMode {
  if (typeof window === 'undefined') {
    return 'dark'
  }

  try {
    const storedMode = window.localStorage.getItem(STORAGE_KEY)
    if (storedMode === 'light' || storedMode === 'dark') {
      return storedMode
    }
  } catch {
    // localStorage 可能在隐私模式下不可用，继续使用系统偏好。
  }

  return window.matchMedia('(prefers-color-scheme: dark)').matches
    ? 'dark'
    : 'light'
}

export function useColorMode() {
  const [colorMode, setColorMode] = useState<ColorMode>(getSystemColorMode)

  useLayoutEffect(() => {
    const root = document.documentElement
    applyLunarPalette(colorMode, root)
    root.classList.toggle('dark', colorMode === 'dark')
    root.dataset.theme = colorMode
    root.style.colorScheme = colorMode
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute('content', lunarPalettes[colorMode].canvas)
  }, [colorMode])

  const toggleColorMode = () => {
    setColorMode((currentMode) => {
      const nextMode = currentMode === 'dark' ? 'light' : 'dark'
      try {
        window.localStorage.setItem(STORAGE_KEY, nextMode)
      } catch {
        // 存储不可用时仍允许当前会话切换主题。
      }
      return nextMode
    })
  }

  return { colorMode, toggleColorMode }
}
