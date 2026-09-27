// Lunar UI 的颜色单一来源：Ant Design、CSS 与图表共用，不改变用户的主题偏好。
export const lunarPalettes = {
  light: {
    canvas: '#f5f6f8', panel: '#f0f2f5', rail: '#f0f2f5',
    surface: '#ffffff', elevated: '#ffffff', hover: '#f0f2f5', selected: '#eeebfa',
    border: '#dfe3ea', borderSoft: '#e7eaf0', borderStrong: '#c1c8d3',
    text: '#161a22', textMuted: '#606a78', textSubtle: '#657080',
    primary: '#6353bc', onPrimary: '#ffffff', context: '#176f80', contextSoft: '#eaf5f7',
    success: '#247547', successSoft: '#e9f2ec', warning: '#875811', warningSoft: '#faf4e9',
    danger: '#b62e44', dangerSoft: '#f7ebed',
    shadow: '0 4px 16px rgb(11 14 20 / 4%)', shadowElevated: '0 12px 36px rgb(11 14 20 / 12%)',
  },
  dark: {
    canvas: '#0b0e14', panel: '#0f141d', rail: '#0b0e14',
    surface: '#111620', elevated: '#171d28', hover: '#1b2230', selected: '#24223b',
    border: '#252d3b', borderSoft: '#202735', borderStrong: '#343e50',
    text: '#e8edf5', textMuted: '#9aa7b8', textSubtle: '#8592a5',
    primary: '#9a8cff', onPrimary: '#151126', context: '#68d5e8', contextSoft: '#142830',
    success: '#63c995', successSoft: '#1c3026', warning: '#e8b86a', warningSoft: '#2a241b',
    danger: '#ff6b7a', dangerSoft: '#35212a',
    shadow: '0 4px 16px rgb(0 0 0 / 8%)', shadowElevated: '0 12px 36px rgb(0 0 0 / 24%)',
  },
} as const

export const lunarTypography = {
  ui: '-apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", "Segoe UI", sans-serif',
  mono: '"SFMono-Regular", "SF Mono", Menlo, Monaco, Consolas, monospace',
}

export const lunarChartColors = {
  light: { activity: ['#c6e8ed', '#8dcbd6', '#499bac', '#176f80'], reasoning: '#887899', cached: '#778696' },
  dark: { activity: ['#234650', '#326d7a', '#479dae', '#68d5e8'], reasoning: '#b7a1d5', cached: '#8399b0' },
}

export function applyLunarPalette(mode: keyof typeof lunarPalettes, root: HTMLElement) {
  for (const [name, value] of Object.entries(lunarPalettes[mode])) {
    root.style.setProperty(`--k-${name.replace(/[A-Z]/g, letter => `-${letter.toLowerCase()}`)}`, value)
  }
}
