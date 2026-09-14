// Token 构成图的 x 轴标签宽度跟随容器实际宽度逐档收窄：容器变窄时截断模型名而不是撑出横向滚动。
// 85 是 grid 的左右留白（left 65 + right 20），8 是相邻标签之间的最小间隙。
const gridHorizontalSpace = 85
const labelGap = 8
const labelWidthSteps = [40, 48, 56, 64, 72, 84, 96, 112, 136]
// 容器宽度尚未测量（或没有类目）时使用默认宽度，与首次渲染前的最宽档位一致。
const defaultLabelWidth = 84

export function compositionLabelWidth(containerWidth: number, categoryCount: number): number {
  if (containerWidth <= 0 || categoryCount <= 0) return defaultLabelWidth
  const available = (containerWidth - gridHorizontalSpace) / categoryCount - labelGap
  return labelWidthSteps.reduce((width, step) => step <= available ? step : width, labelWidthSteps[0])
}
