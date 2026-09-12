import kaguyaLarge from './kaguya-288.webp'
import kaguyaSmall from './kaguya-144.webp'
import userLarge from './lyonmu-288.webp'
import userSmall from './lyonmu-144.webp'

// 头像与欢迎图使用离线压缩的多倍率衍生图（见 docs/optimization-plan.md O18）。
// 1x/2x 变体配合 srcSet，避免把约 4MB 的原图打进首屏。原图仍保留在 assets 目录，
// 需要重新生成时使用仓库根目录的 make avatars 或等价的 cwebp 命令。
export interface ResponsiveImage {
  src: string
  srcSet: string
}

const responsive = (small: string, large: string): ResponsiveImage => ({
  src: small,
  srcSet: `${small} 1x, ${large} 2x`,
})

export const kaguyaAvatar: ResponsiveImage = responsive(kaguyaSmall, kaguyaLarge)
export const userAvatar: ResponsiveImage = responsive(userSmall, userLarge)
