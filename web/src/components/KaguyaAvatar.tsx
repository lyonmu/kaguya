import { kaguyaAvatar } from '../assets/avatars'

// 所有应用品牌与助手标识统一使用辉夜姬头像。
export function KaguyaAvatar({ className = '' }: { className?: string }) {
  return <img {...kaguyaAvatar} className={`kaguya-avatar ${className}`} alt="Kaguya" width={24} height={24} />
}
