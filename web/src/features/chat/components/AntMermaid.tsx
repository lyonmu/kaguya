import { Mermaid, XProvider } from '@ant-design/x'
import xZhCN from '@ant-design/x/locale/zh_CN'
import zhCN from 'antd/locale/zh_CN'

const locale = { ...zhCN, ...xZhCN }
// 桌面应用下载 PNG 没有使用场景，只保留缩放与重置操作；代码视图仍可复制源码。
const actions = { enableZoom: true, enableDownload: false, enableCopy: true }

export function AntMermaid({ code }: { code: string }) {
  return <XProvider locale={locale}><Mermaid actions={actions}>{code}</Mermaid></XProvider>
}
