import { guardFailure, guardSuccess, isArrayOf, isBoolean, isOptional, isRecord, isString, type PayloadGuard } from '../../api/http'
import type { ProjectFilesPage, ReferenceCandidate } from './types'

const isReferenceCandidate = (value: unknown): value is ReferenceCandidate =>
  isRecord(value) && (value.type === 'file' || value.type === 'directory') && isCandidatePath(value.path) && isOptional(value.name, (name): name is string => isString(name) && name.length <= 256)

const isCandidatePath = (value: unknown): value is string => isString(value) && value.length > 0 && value.length <= 4096

/** GET project/:id/files；旧客户端只读 files，新选择器读 items 与 cursor。 */
export const projectFilesGuard: PayloadGuard<ProjectFilesPage> = value => {
  if (!isRecord(value) || !isArrayOf(value.files, isCandidatePath) || value.files.length > 50 || !isBoolean(value.truncated)) return guardFailure('文件引用格式不正确')
  if (!isOptional(value.next_cursor, (cursor): cursor is string => isString(cursor) && cursor.length <= 4096)) return guardFailure('文件引用格式不正确')
  if (!isOptional(value.items, (items): items is ReferenceCandidate[] => isArrayOf(items, isReferenceCandidate) && items.length <= 50)) return guardFailure('文件引用格式不正确')
  if (!isOptional(value.scan_incomplete, isBoolean) || !isOptional(value.cursor_reset, isBoolean) || value.next_cursor && !value.truncated) return guardFailure('文件引用格式不正确')
  return guardSuccess({
    files: value.files,
    truncated: value.truncated,
    items: (value.items as ReferenceCandidate[] | undefined)?.map(item => ({ type: item.type, path: item.path, name: item.name })),
    next_cursor: value.next_cursor as string | undefined,
    scan_incomplete: value.scan_incomplete === true,
    cursor_reset: value.cursor_reset === true,
  })
}
