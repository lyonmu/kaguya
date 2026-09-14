import { lazy, Suspense, useMemo, useState } from 'react'
import { App as AntdApp, ConfigProvider, Spin } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useColorMode } from './app/colorMode'
import { createKaguyaTheme } from './app/theme'
import { AppLayout } from './components/layout/AppLayout'
import type { SystemPage } from './components/layout/AppLayout'
import { ChatProvider } from './features/chat/ChatProvider'
import { ChatPage } from './pages/chat/ChatPage'

const TokenUsagePage = lazy(() => import('./pages/system/TokenUsagePage').then(module => ({ default: module.TokenUsagePage })))
const AIConfigurationPage = lazy(() => import('./pages/system/AIConfigurationPage').then(module => ({ default: module.AIConfigurationPage })))

const SystemInfoPage = lazy(() => import('./pages/system/SystemInfoPage').then(module => ({ default: module.SystemInfoPage })))

function App() {
  const { colorMode, toggleColorMode } = useColorMode()
  const [currentPage, setCurrentPage] = useState<SystemPage>('chat')
  const theme = useMemo(() => createKaguyaTheme(colorMode), [colorMode])

  return (
    <ConfigProvider locale={zhCN} theme={theme}>
      <AntdApp>
      <ChatProvider>
        <AppLayout
          colorMode={colorMode}
          currentPage={currentPage}
          onPageChange={setCurrentPage}
          onToggleColorMode={toggleColorMode}
        >
          <Suspense fallback={<div className="grid h-full place-items-center"><Spin /></div>}>
          {currentPage === 'chat' ? (
            <ChatPage />
          ) : currentPage === 'token-usage' ? (
            <TokenUsagePage />
          ) : currentPage === 'system-info' ? (
            <SystemInfoPage />
          ) : (
            <AIConfigurationPage />
          )}
          </Suspense>
        </AppLayout>
      </ChatProvider>
      </AntdApp>
    </ConfigProvider>
  )
}

export default App
