import { useState, type PropsWithChildren } from 'react'
import {
  ApiOutlined,
  BarChartOutlined,
  // FileSearchOutlined,
  MenuOutlined,
  RobotOutlined,
  RightOutlined,
  SettingOutlined,
} from '@ant-design/icons'
import { Breadcrumb, Button, Drawer, Layout, Menu } from 'antd'
import { BottomActions } from './BottomActions'
import { BottomActionsContext } from './bottomActionsContext'
import type { ColorMode } from '../../app/colorMode'
import kaguyaIcon from '../../assets/kaguya.png'

const { Content, Header, Sider } = Layout

export type SystemPage = 'chat' | 'access-logs' | 'ai-providers' | 'system-info' | 'token-usage'

interface AppLayoutProps extends PropsWithChildren {
  colorMode: ColorMode
  currentPage: SystemPage
  onPageChange: (page: SystemPage) => void
  onToggleColorMode: () => void
}

export function AppLayout({
  children,
  colorMode,
  currentPage,
  onPageChange,
  onToggleColorMode,
}: AppLayoutProps) {
  const isDark = colorMode === 'dark'
  const [actionsCollapsed, setActionsCollapsed] = useState(false)
  const [systemCollapsed, setSystemCollapsed] = useState(false)
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)
  const configurationItems = [
    { key: 'ai-providers', icon: <RobotOutlined />, label: 'AI 提供商' },
    { key: 'system-info', icon: <SettingOutlined />, label: '系统配置' },
  ]

  return (
    <BottomActionsContext.Provider value={{
      isDark, isChat: currentPage === 'chat', collapsed: actionsCollapsed,
      toggleCollapsed: () => setActionsCollapsed(value => !value),
      onChat: () => onPageChange('chat'), onSettings: () => onPageChange('ai-providers'),
      onToggleColorMode,
      avatar: <img alt="Kaguya" className="h-6 w-6 rounded-md object-cover" src={kaguyaIcon} />,
    }}>
    <Layout className="h-svh w-full overflow-hidden bg-k-canvas">

      {currentPage === 'chat' ? <Content className="min-h-0 min-w-0">{children}</Content> : <Layout className="min-h-0 min-w-0 flex-1 bg-k-canvas" hasSider>
        <Sider
          className="border-r border-k-border bg-k-panel! max-[720px]:hidden!"
          theme={isDark ? 'dark' : 'light'}
          width={232}
          collapsed={systemCollapsed}
          collapsedWidth={0}
          trigger={null}
        >
          <div className="flex h-full flex-col px-3 pb-4">
            <div className="border-b border-k-border-soft px-2.5 pt-[23px] pb-5">
              <span className="mb-2 block text-[9px] font-bold tracking-[1.3px] text-k-text-subtle">
                KAGUYA CONSOLE
              </span>
              <h1 className="m-0 text-[17px] font-semibold text-k-text">
                系统管理
              </h1>
              <p className="mt-1 mb-0 text-[11px] text-k-text-subtle">
                System workspace
              </p>
            </div>

            <div className="px-2.5 pt-5 pb-2 text-[10px] font-semibold tracking-[0.7px] text-k-text-subtle">
              AI 配置
            </div>
            <Menu
              className="border-0! bg-transparent!"
              items={configurationItems}
              mode="inline"
              onClick={({ key }) => onPageChange(key as SystemPage)}
              selectedKeys={[currentPage]}
              theme={isDark ? 'dark' : 'light'}
            />

            <div className="px-2.5 pt-5 pb-2 text-[10px] font-semibold tracking-[0.7px] text-k-text-subtle">
              日志与审计
            </div>
            <Menu
              className="border-0! bg-transparent!"
              items={[
                { key: 'token-usage', icon: <BarChartOutlined />, label: '用量分析' },
                // 暂时隐藏访问日志入口，保留页面实现。
                // { key: 'access-logs', icon: <FileSearchOutlined />, label: '访问日志' },
              ]}
              mode="inline"
              onClick={({ key }) => onPageChange(key as SystemPage)}
              selectedKeys={[currentPage]}
              theme={isDark ? 'dark' : 'light'}
            />

            <div className="flex-1" />
            <div className="mx-1 mt-3 flex items-center gap-2.5 rounded-[10px] border border-k-border bg-k-elevated p-2.5">
              <span className="grid h-[30px] w-[30px] shrink-0 place-items-center rounded-lg border border-k-border bg-k-selected text-k-primary">
                <ApiOutlined />
              </span>
              <span className="min-w-0">
                <strong className="block truncate text-[11px] font-semibold text-k-text-muted">
                  System API
                </strong>
                <small className="mt-0.5 block truncate font-mono text-[9px] text-k-text-subtle">
                  /kaguya/api
                </small>
              </span>
            </div>
          </div>
        </Sider>

        <Drawer title="系统管理" placement="left" open={mobileMenuOpen} onClose={() => setMobileMenuOpen(false)}>
          <Menu selectedKeys={[currentPage]} items={[
            ...configurationItems,
            { key: 'token-usage', icon: <BarChartOutlined />, label: '用量分析' },
            // { key: 'access-logs', icon: <FileSearchOutlined />, label: '访问日志' },
          ]} onClick={({ key }) => { onPageChange(key as SystemPage); setMobileMenuOpen(false) }} />
        </Drawer>
        <Layout className="min-w-0 bg-k-canvas">
          <Header className="flex h-[60px]! min-h-[60px] items-center justify-between border-b border-k-border-soft bg-k-surface/95! px-7! leading-none! backdrop-blur-md max-[720px]:h-14! max-[720px]:min-h-14 max-[720px]:px-[18px]!">
            <div className="flex min-w-0 items-center gap-2">
            <Button className="max-[720px]:hidden!" type="text" aria-label={systemCollapsed ? '展开系统菜单' : '收起系统菜单'} aria-expanded={!systemCollapsed} icon={<MenuOutlined />} onClick={() => setSystemCollapsed(value => !value)} />
            <Button className="min-[721px]:hidden!" type="text" aria-label="打开系统菜单" aria-expanded={mobileMenuOpen} icon={<MenuOutlined />} onClick={() => setMobileMenuOpen(true)} />
            <Breadcrumb
              items={[
                { title: '系统管理' },
                { title: currentPage === 'ai-providers' ? 'AI 提供商' : currentPage === 'system-info' ? '系统配置' : currentPage === 'token-usage' ? '用量分析' : '访问日志' },
              ]}
              separator={<RightOutlined className="text-[8px]" />}
            />
            </div>
          </Header>
          <Content className="min-h-0 min-w-0 overflow-auto bg-k-canvas">
            {children}
          </Content>
        </Layout>
      </Layout>}
      {currentPage !== 'chat' && <div className="shrink-0 border-t border-k-border-soft bg-k-panel"><BottomActions /></div>}
    </Layout>
    </BottomActionsContext.Provider>
  )
}
