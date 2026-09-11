import { Mermaid, XProvider } from '@ant-design/x'
import xZhCN from '@ant-design/x/locale/zh_CN'
import zhCN from 'antd/locale/zh_CN'

const locale = { ...zhCN, ...xZhCN }
const actions = { enableZoom: true, enableDownload: true, enableCopy: true }

export function AntMermaid({ code }: { code: string }) {
  return <XProvider locale={locale}><Mermaid actions={actions}>{code}</Mermaid></XProvider>
}
