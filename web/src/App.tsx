import { useMemo, useState } from 'react'
import { App as AntdApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useColorMode } from './app/colorMode'
import { createKaguyaTheme } from './app/theme'
import { AppLayout } from './components/layout/AppLayout'
import type { SystemPage } from './components/layout/AppLayout'
import { AccessLogPage } from './pages/system/AccessLogPage'
import { ProviderManagementPage } from './pages/system/ProviderManagementPage'

function App() {
  const { colorMode, toggleColorMode } = useColorMode()
  const [currentPage, setCurrentPage] = useState<SystemPage>('access-logs')
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
          {currentPage === 'ai-providers' ? (
            <ProviderManagementPage />
          ) : (
            <AccessLogPage />
          )}
        </AppLayout>
      </AntdApp>
    </ConfigProvider>
  )
}

export default App
