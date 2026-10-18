import { guardFailure, guardSuccess, isRecord } from '../../api/transport'
import type { PayloadGuard } from '../../api/transport'
import type { ModelLabelOption } from './types'

/** 空标签归一化为数组；共享缓存只保留非secret合同字段。 */
export const modelLabelsGuard: PayloadGuard<ModelLabelOption[]> = value => {
  if (value === null) return guardSuccess([])
  if (!Array.isArray(value)) return guardFailure('模型标签格式不正确')
  const labels: ModelLabelOption[] = []
  for (const item of value) {
    if (!isRecord(item) || typeof item.label !== 'string' || typeof item.value !== 'string' || typeof item.provider_id !== 'string' || typeof item.provider_name !== 'string' || typeof item.model_id !== 'string' || typeof item.is_default !== 'boolean') return guardFailure('模型标签格式不正确')
    labels.push({ label: item.label, value: item.value, provider_id: item.provider_id, provider_name: item.provider_name, model_id: item.model_id, is_default: item.is_default })
  }
  return guardSuccess(labels)
}
