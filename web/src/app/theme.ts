import { theme } from 'antd'
import type { ThemeConfig } from 'antd'
import type { ColorMode } from './colorMode'
import { lunarPalettes, lunarTypography } from './lunar'

export function createKaguyaTheme(colorMode: ColorMode): ThemeConfig {
  const isDark = colorMode === 'dark'
  const palette = lunarPalettes[colorMode]

  return {
    algorithm: isDark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: palette.primary,
      colorInfo: palette.context,
      colorSuccess: palette.success,
      colorWarning: palette.warning,
      colorError: palette.danger,
      colorBgBase: palette.canvas,
      colorBgLayout: palette.canvas,
      colorBgContainer: palette.surface,
      colorBgElevated: palette.elevated,
      colorBorder: palette.border,
      colorBorderSecondary: palette.borderSoft,
      colorText: palette.text,
      colorTextSecondary: palette.textMuted,
      colorTextTertiary: palette.textSubtle,
      colorTextQuaternary: palette.textSubtle,
      borderRadius: 9,
      borderRadiusSM: 6,
      borderRadiusLG: 12,
      fontFamily: lunarTypography.ui,
      fontFamilyCode: lunarTypography.mono,
      fontSize: 14,
      fontSizeSM: 12,
      fontSizeHeading2: 22,
      fontSizeHeading3: 18,
      fontSizeHeading4: 16,
      fontWeightStrong: 500,
      motionDurationFast: '0.12s',
      motionDurationMid: '0.18s',
      motionDurationSlow: '0.22s',
      boxShadow: palette.shadowElevated,
      boxShadowSecondary: palette.shadow,
      // 保留现有桌面信息密度和控件交互尺寸。
      controlHeight: 32,
    },
    components: {
      Layout: {
        bodyBg: palette.canvas,
        headerBg: palette.surface,
        siderBg: palette.panel,
      },
      Menu: {
        itemBg: 'transparent',
        itemColor: palette.textMuted,
        itemHoverBg: palette.hover,
        itemHoverColor: palette.text,
        itemSelectedBg: palette.selected,
        itemSelectedColor: palette.primary,
        darkItemBg: 'transparent',
        darkItemColor: palette.textMuted,
        darkItemHoverBg: palette.hover,
        darkItemHoverColor: palette.text,
        darkItemSelectedBg: palette.selected,
        darkItemSelectedColor: palette.primary,
        itemBorderRadius: 6,
        itemHeight: 34,
        itemMarginBlock: 2,
        itemMarginInline: 0,
      },
      Table: {
        headerBg: palette.panel,
        headerColor: palette.textMuted,
        borderColor: palette.borderSoft,
        rowHoverBg: palette.hover,
        cellPaddingBlock: 10,
        cellPaddingInline: 12,
        cellPaddingBlockSM: 6,
        cellPaddingInlineSM: 8,
      },
      Card: {
        colorBgContainer: palette.surface,
        bodyPadding: 20,
        headerHeight: 46,
        headerPadding: 20,
      },
      Form: {
        itemMarginBottom: 18,
        verticalLabelPadding: '0 0 4px',
      },
      Tabs: {
        horizontalItemGutter: 24,
        horizontalItemPadding: '9px 0',
        horizontalMargin: '0 0 10px 0',
      },
      Descriptions: {
        itemPaddingBottom: 12,
      },
      Input: {
        activeBorderColor: palette.primary,
        hoverBorderColor: palette.borderStrong,
      },
      DatePicker: {
        activeBorderColor: palette.primary,
        hoverBorderColor: palette.borderStrong,
      },
      Button: {
        defaultBg: palette.surface,
        defaultBorderColor: palette.border,
        defaultHoverBg: palette.hover,
        primaryColor: palette.onPrimary,
        primaryShadow: 'none',
        defaultShadow: 'none',
      },
      Pagination: {
        itemActiveBg: palette.selected,
        itemSize: 30,
      },
      Tooltip: { colorBgSpotlight: palette.elevated, colorTextLightSolid: palette.text },
      Drawer: { colorBgElevated: palette.surface },
      Modal: { contentBg: palette.elevated, headerBg: palette.elevated },
    },
  }
}
