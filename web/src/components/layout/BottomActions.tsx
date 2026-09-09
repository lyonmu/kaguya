import { useContext } from 'react'
import { BottomActionsContext } from './bottomActionsContext'
import { Button, Tooltip } from 'antd'
import { CommentOutlined, LeftOutlined, MenuOutlined, MoonOutlined, ReloadOutlined, SettingOutlined, SunOutlined } from '@ant-design/icons'

export function BottomActions({ onRefresh, loading }: { onRefresh?: () => void; loading?: boolean }) {
  const navigation = useContext(BottomActionsContext)
  return <div className="flex items-center gap-1 p-2" role="group" aria-label="快捷操作">
    {!navigation?.collapsed && <>
      {navigation && <>
        <Tooltip title="Kaguya · 返回对话"><Button type="text" aria-label="Kaguya" onClick={navigation.onChat} className="p-1!">{navigation.avatar}</Button></Tooltip>
        <Tooltip title="对话管理"><Button type={navigation.isChat ? 'primary' : 'text'} aria-label="对话管理" icon={<CommentOutlined />} onClick={navigation.onChat} /></Tooltip>
        <Tooltip title="系统管理"><Button type={!navigation.isChat ? 'primary' : 'text'} aria-label="系统管理" icon={<SettingOutlined />} onClick={navigation.onSettings} /></Tooltip>
        <Tooltip title={navigation.isDark ? '切换到明亮模式' : '切换到暗黑模式'}><Button type="text" aria-label="切换颜色模式" icon={navigation.isDark ? <SunOutlined /> : <MoonOutlined />} onClick={navigation.onToggleColorMode} /></Tooltip>
      </>}
      {onRefresh && <Tooltip title="刷新列表"><Button type="text" aria-label="刷新列表" icon={<ReloadOutlined />} loading={loading} onClick={onRefresh} /></Tooltip>}
    </>}
    {navigation && <Tooltip title={navigation.collapsed ? '展开快捷操作' : '收起快捷操作'}><Button type="text" aria-label={navigation.collapsed ? '展开快捷操作' : '收起快捷操作'} aria-expanded={!navigation.collapsed} icon={navigation.collapsed ? <MenuOutlined /> : <LeftOutlined />} onClick={navigation.toggleCollapsed} /></Tooltip>}
  </div>
}
