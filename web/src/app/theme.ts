import { theme } from 'antd'
import type { ThemeConfig } from 'antd'
import type { ColorMode } from './colorMode'

const darkPalette = {
  canvas: '#0e1116',
  panel: '#101318',
  surface: '#11161d',
  elevated: '#171d25',
  border: '#29323d',
  borderSoft: '#202731',
  text: '#edf2f7',
  muted: '#8e99a8',
  hover: '#151c25',
  selected: '#192230',
}

const lightPalette = {
  canvas: '#f4f7fb',
  panel: '#f8fafc',
  surface: '#ffffff',
  elevated: '#ffffff',
  border: '#d9e1eb',
  borderSoft: '#e8edf3',
  text: '#172033',
  muted: '#64748b',
  hover: '#f2f6fb',
  selected: '#e8f1ff',
}

export function createKaguyaTheme(colorMode: ColorMode): ThemeConfig {
  const isDark = colorMode === 'dark'
  const palette = isDark ? darkPalette : lightPalette

  return {
    algorithm: isDark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: isDark ? '#6ea8fe' : '#2563eb',
      colorInfo: isDark ? '#6ea8fe' : '#2563eb',
      colorSuccess: '#37a86b',
      colorWarning: '#d99127',
      colorError: '#e5484d',
      colorBgBase: palette.canvas,
      colorBgLayout: palette.canvas,
      colorBgContainer: palette.surface,
      colorBgElevated: palette.elevated,
      colorBorder: palette.border,
      colorBorderSecondary: palette.borderSoft,
      colorText: palette.text,
      colorTextSecondary: palette.muted,
      borderRadius: 9,
      borderRadiusLG: 12,
      fontFamily:
        'Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
      fontSize: 14,
      controlHeight: 36,
    },
    components: {
      Layout: {
        bodyBg: palette.canvas,
        headerBg: palette.surface,
        siderBg: palette.panel,
      },
      Menu: {
        itemBg: 'transparent',
        itemColor: palette.muted,
        itemHoverBg: palette.hover,
        itemHoverColor: palette.text,
        itemSelectedBg: palette.selected,
        itemSelectedColor: isDark ? '#e8f1ff' : '#1d4f91',
        darkItemBg: 'transparent',
        darkItemColor: palette.muted,
        darkItemHoverBg: palette.hover,
        darkItemHoverColor: palette.text,
        darkItemSelectedBg: palette.selected,
        darkItemSelectedColor: '#e8f1ff',
        itemBorderRadius: 9,
        itemMarginInline: 0,
      },
      Table: {
        headerBg: isDark ? '#121820' : '#f7f9fc',
        headerColor: palette.muted,
        borderColor: palette.borderSoft,
        rowHoverBg: palette.hover,
      },
      Card: {
        colorBgContainer: palette.surface,
      },
      Input: {
        activeBorderColor: isDark ? '#526f95' : '#5b8fd8',
        hoverBorderColor: isDark ? '#405675' : '#7aa5df',
      },
      DatePicker: {
        activeBorderColor: isDark ? '#526f95' : '#5b8fd8',
        hoverBorderColor: isDark ? '#405675' : '#7aa5df',
      },
      Button: {
        defaultBg: isDark ? '#151b23' : '#ffffff',
        defaultBorderColor: palette.border,
        defaultHoverBg: palette.hover,
      },
      Pagination: {
        itemActiveBg: palette.selected,
      },
    },
  }
}
