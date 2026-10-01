import { guardFailure, guardSuccess, isArrayOf, isBoolean, isOptional, isRecord, isString, type PayloadGuard } from '../../api/http'
import type { ProjectFilesPage, ReferenceCandidate } from './types'

const isReferenceCandidate = (value: unknown): value is ReferenceCandidate =>
  isRecord(value) && (value.type === 'file' || value.type === 'directory') && isString(value.path) && isOptional(value.name, isString)

/** GET project/:id/files；旧客户端只读 files，新选择器读 items 与 cursor。 */
export const projectFilesGuard: PayloadGuard<ProjectFilesPage> = value => {
  if (!isRecord(value) || !isArrayOf(value.files, isString) || !isBoolean(value.truncated)) return guardFailure('文件引用格式不正确')
  if (!isOptional(value.next_cursor, isString)) return guardFailure('文件引用格式不正确')
  if (!isOptional(value.items, (items): items is ReferenceCandidate[] => isArrayOf(items, isReferenceCandidate))) return guardFailure('文件引用格式不正确')
  return guardSuccess({
    files: value.files,
    truncated: value.truncated,
    items: value.items as ReferenceCandidate[] | undefined,
    next_cursor: value.next_cursor as string | undefined,
  })
}
