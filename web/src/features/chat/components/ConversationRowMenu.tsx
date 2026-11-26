import { Button, Dropdown } from 'antd'
import { DeleteOutlined, MoreOutlined } from '@ant-design/icons'

export function ConversationRowMenu({ title, disabled, onRemove }: { title: string; disabled: boolean; onRemove: () => void }) {
  return <Dropdown trigger={['click']} menu={{
    items: [{ key: 'delete', label: '删除对话', danger: true, icon: <DeleteOutlined /> }],
    onClick: onRemove,
  }}>
    <Button className="chat-session-menu" type="text" size="small" aria-label={`${title} 会话操作`} icon={<MoreOutlined />} disabled={disabled} />
  </Dropdown>
}
