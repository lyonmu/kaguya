import type { Conversation, TitleEvent, TitleSnapshot, TitleUpdate } from './types'

type TitleFields = Pick<Conversation, 'id' | 'title' | 'title_revision' | 'title_job_status' | 'title_job_revision'>

/** 只合并现存实体；同版本冲突保留确认值，由调用方有界补查。 */
export function mergeTitle<T extends TitleFields>(current: T, update: TitleUpdate): { value: T & TitleFields; conflict: boolean } {
  if (current.id !== update.id) return { value: current, conflict: false }
  let value = current
  let conflict = false
  if (update.title !== undefined) {
    const revision = current.title_revision
    if (revision === undefined || (update.revision !== undefined && update.revision > revision)) {
      value = { ...value, title: update.title, title_revision: update.revision }
    } else if (update.revision === revision && update.title !== current.title) conflict = true
  }
  if (update.job_status !== undefined) {
    const revision = current.title_job_revision
    if (revision === undefined || (update.job_revision !== undefined && update.job_revision > revision)) {
      value = { ...value, title_job_status: update.job_status, title_job_revision: update.job_revision }
    } else if (update.job_revision === revision && update.job_status !== (current.title_job_status ?? null)) conflict = true
  }
  return { value, conflict }
}

export function titleFromConversation(value: TitleFields): TitleUpdate {
  return { id: value.id, title: value.title, revision: value.title_revision,
    job_status: value.title_job_status ?? (value.title_job_revision === undefined ? undefined : null), job_revision: value.title_job_revision }
}

export function titleFromSnapshot(value: TitleSnapshot): TitleUpdate {
  return { id: value.id, title: value.title, revision: value.title_revision, job_status: value.job_status, job_revision: value.job_revision }
}

export function titleFromEvent(value: TitleEvent): TitleUpdate {
  return { id: value.conversation_id, title: value.type === 'conversation.title.updated' ? value.title : undefined,
    revision: value.type === 'conversation.title.updated' ? value.revision : undefined, job_status: value.job_status, job_revision: value.job_revision }
}
