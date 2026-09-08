import { lazy, Suspense, useMemo, useState } from 'react'
import { App as AntdApp, ConfigProvider, Spin } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useColorMode } from './app/colorMode'
import { createKaguyaTheme } from './app/theme'
import { AppLayout } from './components/layout/AppLayout'
import type { SystemPage } from './components/layout/AppLayout'
import { ChatPage } from './pages/chat/ChatPage'

const AccessLogPage = lazy(() => import('./pages/system/AccessLogPage').then(module => ({ default: module.AccessLogPage })))
const ProviderManagementPage = lazy(() => import('./pages/system/ProviderManagementPage').then(module => ({ default: module.ProviderManagementPage })))

function App() {
  const { colorMode, toggleColorMode } = useColorMode()
  const [currentPage, setCurrentPage] = useState<SystemPage>('chat')
  const theme = useMemo(() => createKaguyaTheme(colorMode), [colorMode])

  return (
    <ConfigProvider locale={zhCN} theme={theme}>
      <AntdApp>
        <AppLayout
          colorMode={colorMode}
          currentPage={currentPage}
          onPageChange={setCurrentPage}
          onToggleColorMode={toggleColorMode}
        >
          <Suspense fallback={<div className="grid h-full place-items-center"><Spin /></div>}>
          {currentPage === 'chat' ? (
            <ChatPage />
          ) : currentPage === 'ai-providers' ? (
            <ProviderManagementPage />
          ) : (
            <AccessLogPage />
          )}
          </Suspense>
        </AppLayout>
      </AntdApp>
    </ConfigProvider>
  )
}

export default App
