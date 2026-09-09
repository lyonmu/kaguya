import { createContext, type ReactNode } from 'react'

export const BottomActionsContext = createContext<{
  isDark: boolean
  isChat: boolean
  collapsed: boolean
  toggleCollapsed: () => void
  onChat: () => void
  onSettings: () => void
  onToggleColorMode: () => void
  avatar: ReactNode
} | null>(null)
