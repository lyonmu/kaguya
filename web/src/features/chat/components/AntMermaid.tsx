import { useMemo } from 'react'
import { theme } from 'antd'
import { lunarTypography } from '../../../app/lunar'
import { Mermaid, XProvider } from '@ant-design/x'
import xZhCN from '@ant-design/x/locale/zh_CN'
import zhCN from 'antd/locale/zh_CN'

const locale = { ...zhCN, ...xZhCN }
// 桌面应用下载 PNG 没有使用场景，只保留缩放与重置操作；代码视图仍可复制源码。
const actions = { enableZoom: true, enableDownload: false, enableCopy: true }

export function AntMermaid({ code }: { code: string }) {
  const { token } = theme.useToken()
  const config = useMemo(() => ({
    theme: 'base' as const,
    securityLevel: 'strict' as const,
    fontFamily: lunarTypography.ui,
    themeVariables: {
      background: token.colorBgContainer,
      primaryColor: token.colorBgElevated,
      primaryTextColor: token.colorText,
      primaryBorderColor: token.colorPrimary,
      secondaryColor: token.colorBgLayout,
      tertiaryColor: token.colorBgContainer,
      lineColor: token.colorTextSecondary,
      textColor: token.colorText,
      edgeLabelBackground: token.colorBgContainer,
    },
  }), [token])
  return <XProvider locale={locale}><Mermaid actions={actions} config={config}>{code}</Mermaid></XProvider>
}
