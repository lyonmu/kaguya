import { useState } from 'react'
import { Alert, App, Button, Card, Input, Popconfirm, Select, Space } from 'antd'
import { updateTLS } from '../../features/system-info/api'
import type { TLSInfo, TLSPayload } from '../../features/system-info/types'

export function TLSConfigPanel({ info, onSaved }: { info?: TLSInfo; onSaved: (info: TLSInfo) => void }) {
  const { message } = App.useApp()
  const [hosts, setHosts] = useState<string[]>()
  const [certificate, setCertificate] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [changed, setChanged] = useState(false)
  const save = async (payload: TLSPayload) => {
    setSaving(true)
    try {
      const result = await updateTLS(payload)
      onSaved(result)
      setCertificate(''); setPrivateKey(''); setChanged(true)
      void message.success('证书已保存，重启服务后生效')
    } catch (error) { void message.error(error instanceof Error ? error.message : '证书保存失败') }
    finally { setSaving(false) }
  }
  return <Card title="HTTPS / TLS 1.3" style={{ marginTop: 20 }}>
    <p>服务仅接受 TLS 1.3。证书和私钥保存在加密数据库，私钥不会由查询接口返回。证书更换后需重启服务。</p>
    <p>自签名证书需要在访问设备上信任；请核对指纹后导入公钥证书。HTTPS 不替代登录和访问控制。</p>
    {changed && <Alert className="mb-4" type="warning" showIcon title="证书已更新，请重启服务并重新信任新证书" />}
    {info?.fingerprint && <div className="mb-4 break-all text-sm">
      <div>到期时间：{new Date(info.not_after).toLocaleString()}</div>
      <div>SHA-256：{info.fingerprint}</div>
      <div>适用地址：{info.hosts.join('、')}</div>
      <Button className="mt-2" href={`data:application/x-pem-file;charset=utf-8,${encodeURIComponent(info.certificate_pem)}`} download="kaguya.crt">下载公钥证书</Button>
    </div>}
    <label className="mb-2 block" htmlFor="tls-hosts">自签名证书的域名或 IP 地址</label>
    <Select id="tls-hosts" aria-label="证书适用地址" mode="tags" className="mb-3 w-full" value={hosts ?? info?.hosts ?? ['localhost', '127.0.0.1', '::1']} onChange={setHosts} disabled={saving} placeholder="输入实际访问的域名或 IP，按回车添加" />
    <Popconfirm title="重新生成证书？" description="重启后访问设备需要重新信任新证书。" onConfirm={() => save({ generate: true, hosts: hosts ?? info?.hosts ?? [] })} okText="生成并保存" cancelText="取消" disabled={saving}>
      <Button loading={saving}>重新生成自签名证书</Button>
    </Popconfirm>
    <details className="mt-5">
      <summary className="cursor-pointer">导入已有证书与私钥</summary>
      <Space orientation="vertical" className="mt-3 w-full">
        <Input.TextArea aria-label="TLS 证书 PEM" placeholder="证书 PEM（完整证书链，叶证书在前）" rows={4} value={certificate} maxLength={131072} onChange={event => setCertificate(event.target.value)} disabled={saving} spellCheck={false} />
        <Input.TextArea aria-label="TLS 私钥 PEM" placeholder="匹配的私钥 PEM；保存后清空" rows={4} value={privateKey} maxLength={32768} onChange={event => setPrivateKey(event.target.value)} disabled={saving} autoComplete="off" spellCheck={false} />
        <Button loading={saving} disabled={!certificate.trim() || !privateKey.trim()} onClick={() => void save({ certificate_pem: certificate, private_key_pem: privateKey })}>保存导入证书</Button>
      </Space>
    </details>
  </Card>
}
