import { useMemo } from 'react'
import { App as AntdApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useColorMode } from './app/colorMode'
import { createKaguyaTheme } from './app/theme'
import { AppLayout } from './components/layout/AppLayout'
import { AccessLogPage } from './pages/system/AccessLogPage'

function App() {
  const { colorMode, toggleColorMode } = useColorMode()
  const theme = useMemo(() => createKaguyaTheme(colorMode), [colorMode])

  return (
    <ConfigProvider locale={zhCN} theme={theme}>
      <AntdApp>
        <AppLayout colorMode={colorMode} onToggleColorMode={toggleColorMode}>
          <AccessLogPage />
        </AppLayout>
      </AntdApp>
    </ConfigProvider>
  )
}

export default App
