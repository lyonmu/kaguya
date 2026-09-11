import { useMemo } from 'react'
import { Cascader } from 'antd'
import type { ModelLabelOption } from './types'

interface ModelOption {
  value: string
  label: string
  modelId?: string
  children?: ModelOption[]
}

interface Props {
  models: ModelLabelOption[]
  value?: string
  onChange?: (value: string) => void
  placeholder?: string
  defaultOption?: boolean
  disabled?: boolean
  loading?: boolean
  className?: string
  id?: string
  displayLeafOnly?: boolean
  placement?: 'bottomLeft' | 'bottomRight' | 'topLeft' | 'topRight'
  'aria-label'?: string
}

export function ModelCascader({ models, value, onChange, placeholder = '未配置', defaultOption = false, displayLeafOnly = false, ...props }: Props) {
  const defaultModel = models.find(model => model.is_default)
  const options = useMemo(() => {
    const providers = new Map<string, ModelOption>()
    for (const model of models) {
      let provider = providers.get(model.provider_id)
      if (!provider) {
        provider = { value: model.provider_id, label: model.provider_name, children: [] }
        providers.set(model.provider_id, provider)
      }
      provider.children!.push({ value: model.value, label: model.label, modelId: model.model_id })
    }
    return [...providers.values()]
  }, [models])
  // 空值表示跟随系统默认模型：收起时展示该模型，下拉里也把它标记为选中项。
  const current = models.find(model => model.value === value) ?? (defaultOption && !value ? defaultModel : undefined)
  return <Cascader<ModelOption>
    {...props}
    style={props.className ? undefined : { width: '100%' }}
    options={options}
    value={current ? [current.provider_id, current.value] : value ? [value] : []}
    onChange={path => {
      const next = path?.length === 2 ? String(path[1]) : ''
      // 直接选中系统默认模型时仍提交空值，保持跟随后端配置的语义。
      onChange?.(defaultOption && defaultModel && next === defaultModel.value ? '' : next)
    }}
    displayRender={labels => (displayLeafOnly ? labels.at(-1) : labels.join(' / ')) ?? ''}
    allowClear={!defaultOption}
    placeholder={defaultOption && !defaultModel ? '未配置默认模型' : placeholder}
    showSearch={{
      filter: (input, path) => path.some(option =>
        `${option.label} ${option.modelId ?? ''}`.toLowerCase().includes(input.trim().toLowerCase()),
      ),
    }}
    notFoundContent="未找到模型"
  />
}
