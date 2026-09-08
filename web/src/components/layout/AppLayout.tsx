import type { PropsWithChildren } from 'react'
import {
  ApiOutlined,
  CommentOutlined,
  FileSearchOutlined,
  MoonOutlined,
  RobotOutlined,
  RightOutlined,
  SettingOutlined,
  SunOutlined,
} from '@ant-design/icons'
import { Breadcrumb, Button, Layout, Menu, Tooltip } from 'antd'
import type { ColorMode } from '../../app/colorMode'
import kaguyaIcon from '../../assets/kaguya.png'

const { Content, Header, Sider } = Layout

export type SystemPage = 'chat' | 'access-logs' | 'ai-providers'

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

  return (
    <Layout className="h-svh w-full overflow-hidden bg-k-canvas" hasSider>
      <Sider
        className="z-10 border-r border-k-border bg-k-rail!"
        theme={isDark ? 'dark' : 'light'}
        width={72}
      >
        <div className="flex h-full flex-col items-center px-2.5 py-3.5">
          <Tooltip placement="right" title="Kaguya Agent Console">
            <div
              aria-label="Kaguya Agent Console"
              className="h-11 w-11 cursor-default overflow-hidden rounded-[13px] border border-k-border bg-k-elevated p-0.5 shadow-lg shadow-black/10"
            >
              <img
                alt="Kaguya"
                className="block h-full w-full rounded-[10px] object-cover object-center"
                src={kaguyaIcon}
              />
            </div>
          </Tooltip>

          <div className="my-3 h-px w-7 bg-k-border-soft" />

          <Tooltip placement="right" title="对话管理">
            <Button
              aria-label="对话管理"
              className={`mb-2 h-11! w-11! rounded-xl! ${currentPage === 'chat' ? 'bg-k-selected! text-k-primary!' : 'text-k-text-muted!'}`}
              icon={<CommentOutlined />}
              type="text"
              onClick={() => onPageChange('chat')}
            />
          </Tooltip>
          <Tooltip placement="right" title="系统管理">
            <div className="relative">
              {currentPage !== 'chat' && <span className="absolute top-[11px] -left-[11px] h-[22px] w-[3px] rounded-full bg-k-primary" />}
              <Button
                aria-label="系统管理"
                className={`h-11! w-11! rounded-xl! ${currentPage !== 'chat' ? 'bg-k-selected! text-k-primary!' : 'text-k-text-muted!'}`}
                icon={<SettingOutlined />}
                type="text"
                onClick={() => onPageChange('access-logs')}
              />
            </div>
          </Tooltip>

          <div className="flex-1" />
          <Tooltip title={isDark ? '切换到明亮模式' : '切换到暗黑模式'}>
            <Button className="mb-4" type="text" aria-label="切换颜色模式" icon={isDark ? <SunOutlined /> : <MoonOutlined />} onClick={onToggleColorMode} />
          </Tooltip>
          <Tooltip placement="right" title="Console ready">
            <span
              aria-label="Console ready"
              className="mb-4 h-2 w-2 rounded-full bg-emerald-500 shadow-[0_0_0_4px_rgb(16_185_129_/_10%)]"
            />
          </Tooltip>
          <div className="grid h-[34px] w-[34px] place-items-center rounded-full border border-k-border bg-k-elevated text-xs font-bold text-k-text-muted">
            K
          </div>
        </div>
      </Sider>

      {currentPage === 'chat' ? <Content className="min-h-0 min-w-0">{children}</Content> : <Layout className="min-w-0 bg-k-canvas" hasSider>
        <Sider
          className="border-r border-k-border bg-k-panel! max-[720px]:hidden!"
          theme={isDark ? 'dark' : 'light'}
          width={232}
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
              items={[
                {
                  key: 'ai-providers',
                  icon: <RobotOutlined />,
                  label: 'AI 提供商',
                },
              ]}
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
                {
                  key: 'access-logs',
                  icon: <FileSearchOutlined />,
                  label: '访问日志',
                },
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

        <Layout className="min-w-0 bg-k-canvas">
          <Header className="flex h-[60px]! min-h-[60px] items-center justify-between border-b border-k-border-soft bg-k-surface/95! px-7! leading-none! backdrop-blur-md max-[720px]:h-14! max-[720px]:min-h-14 max-[720px]:px-[18px]!">
            <Breadcrumb
              items={[
                { title: '系统管理' },
                { title: currentPage === 'ai-providers' ? 'AI 提供商' : '访问日志' },
              ]}
              separator={<RightOutlined className="text-[8px]" />}
            />
            <div className="flex items-center gap-2">
              <div className="mr-1 flex items-center gap-2 text-[11px] text-k-text-subtle max-[520px]:hidden">
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                <span>Kaguya Agent Console</span>
              </div>
              <Tooltip title={isDark ? '切换到明亮模式' : '切换到暗黑模式'}>
                <Button
                  aria-label={isDark ? '切换到明亮模式' : '切换到暗黑模式'}
                  className="border-k-border! bg-k-elevated! text-k-text-muted!"
                  icon={isDark ? <SunOutlined /> : <MoonOutlined />}
                  onClick={onToggleColorMode}
                  shape="circle"
                />
              </Tooltip>
            </div>
          </Header>
          <Content className="min-h-0 min-w-0 overflow-auto bg-k-canvas">
            {children}
          </Content>
        </Layout>
      </Layout>}
    </Layout>
  )
}
