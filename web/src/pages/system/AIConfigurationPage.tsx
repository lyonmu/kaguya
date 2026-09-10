import { lazy, Suspense } from 'react'
import { Spin, Tabs } from 'antd'

const ProviderManagementPage = lazy(() => import('./ProviderManagementPage').then(module => ({ default: module.ProviderManagementPage })))
const MCPManagementPanel = lazy(() => import('./MCPManagementPanel').then(module => ({ default: module.MCPManagementPanel })))

export function AIConfigurationPage() {
  return <Suspense fallback={<div className="p-8 text-center"><Spin /></div>}>
    <Tabs className="w-full" tabBarStyle={{ padding: '12px 32px 0', marginBottom: 0 }} destroyOnHidden items={[
      { key: 'providers', label: '提供商与模型', children: <ProviderManagementPage /> },
      { key: 'mcp', label: 'MCP 管理', children: <MCPManagementPanel /> },
    ]} />
  </Suspense>
}
