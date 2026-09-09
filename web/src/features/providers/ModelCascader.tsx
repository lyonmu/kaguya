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
  'aria-label'?: string
}

export function ModelCascader({ models, value, onChange, placeholder = '未配置', defaultOption = false, ...props }: Props) {
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
    return [...(defaultOption ? [{ value: '', label: '默认模型' }] : []), ...providers.values()]
  }, [models, defaultOption])
  const selected = models.find(model => model.value === value)
  return <Cascader<ModelOption>
    {...props}
    style={props.className ? undefined : { width: '100%' }}
    options={options}
    value={selected ? [selected.provider_id, selected.value] : value ? [value] : defaultOption ? [''] : []}
    onChange={path => onChange?.(path?.length === 2 ? String(path[1]) : '')}
    allowClear={!defaultOption}
    placeholder={placeholder}
    showSearch={{
      filter: (input, path) => path.some(option =>
        `${option.label} ${option.modelId ?? ''}`.toLowerCase().includes(input.trim().toLowerCase()),
      ),
    }}
    notFoundContent="未找到模型"
  />
}
