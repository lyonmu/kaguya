import { fetchTitleSnapshots } from './api'
import { titleFromSnapshot } from './titleMerge'
import type { TitleSnapshots, TitleUpdate } from './types'

interface Handlers {
  update: (update: TitleUpdate) => boolean
  missing: (id: string) => void
  error: (message: string) => void
  clearError?: () => void
}
interface Scope { members: Map<string, symbol>; handlers: Handlers }
interface Batch { ids: string[]; owners: Map<string, Map<Scope, symbol>>; controller: AbortController }
interface Generation {
  signal: AbortSignal
  pending: Set<string>
  inflight: Map<string, Batch>
  batches: Set<Batch>
  conflicts: Set<string>
  reported: Set<string>
}

/** Provider私有协调器仅保存ID/scope，不保存列表或session实体。 */
export class TitleSync {
  private scopes = new Set<Scope>()
  private generation?: Generation
  private fetchSnapshots: (ids: string[], signal: AbortSignal) => Promise<TitleSnapshots>

  constructor(fetchSnapshots: (ids: string[], signal: AbortSignal) => Promise<TitleSnapshots> = fetchTitleSnapshots) { this.fetchSnapshots = fetchSnapshots }

  register(handlers: Handlers) {
    const scope: Scope = { members: new Map(), handlers }
    this.scopes.add(scope)
    let closed = false
    return {
      setIDs: (ids: readonly string[]) => {
        if (closed) return
        const next = new Set(ids)
        for (const id of scope.members.keys()) if (!next.has(id)) { scope.members.delete(id); this.prune(id) }
        for (const id of next) {
          if (!id || scope.members.has(id)) continue
          scope.members.set(id, Symbol(id))
          // 同ID只有一个在途请求；新scope的missing不能沿用旧请求的所有权。
          const inflight = this.generation?.inflight.get(id)
          if (!inflight || inflight.controller.signal.aborted) this.generation?.pending.add(id)
        }
        this.pump()
      },
      close: () => {
        if (closed) return
        closed = true
        this.scopes.delete(scope)
        for (const id of scope.members.keys()) this.prune(id)
        scope.members.clear()
      },
    }
  }

  connect(signal: AbortSignal) {
    this.disconnect()
    const generation: Generation = { signal, pending: new Set(), inflight: new Map(), batches: new Set(), conflicts: new Set(), reported: new Set() }
    this.generation = generation
    for (const scope of this.scopes) {
      scope.handlers.clearError?.()
      for (const id of scope.members.keys()) generation.pending.add(id)
    }
    const disconnect = () => { if (this.generation === generation) this.disconnect() }
    signal.addEventListener('abort', disconnect, { once: true })
    if (signal.aborted) disconnect()
    else this.pump()
    return () => { signal.removeEventListener('abort', disconnect); disconnect() }
  }

  private disconnect() {
    const generation = this.generation
    this.generation = undefined
    if (generation) for (const batch of generation.batches) batch.controller.abort()
  }

  private owners(id: string) {
    const owners = new Map<Scope, symbol>()
    for (const scope of this.scopes) {
      const token = scope.members.get(id)
      if (token) owners.set(scope, token)
    }
    return owners
  }

  private prune(id: string) {
    if (this.owners(id).size) return
    const generation = this.generation
    if (!generation) return
    generation.pending.delete(id)
    generation.conflicts.delete(id)
    generation.reported.delete(id)
    const batch = generation.inflight.get(id)
    if (batch && batch.ids.every(value => !this.owners(value).size)) batch.controller.abort()
  }

  update(update: TitleUpdate) {
    let conflict = false
    for (const scope of [...this.scopes]) if (scope.members.has(update.id)) conflict = scope.handlers.update(update) || conflict
    if (conflict) this.conflict(update.id)
  }

  conflict(id: string) {
    const generation = this.generation
    if (!generation || !this.owners(id).size) return
    if (!generation.conflicts.has(id)) {
      generation.conflicts.add(id)
      generation.pending.add(id)
      this.pump()
    } else if (!generation.reported.has(id)) {
      generation.reported.add(id)
      for (const scope of this.scopes) if (scope.members.has(id)) scope.handlers.error('标题同步数据不一致，请重新加载会话')
    }
  }

  /** 已成功删除的实体从全部当前scope移除，事件不会创建新条目。 */
  remove(id: string) {
    for (const scope of [...this.scopes]) {
      if (!scope.members.delete(id)) continue
      scope.handlers.missing(id)
    }
    this.prune(id)
  }

  private pump() {
    const generation = this.generation
    if (!generation || generation.signal.aborted) return
    while (generation.batches.size < 4) {
      const ids: string[] = []
      for (const id of generation.pending) {
        if (generation.inflight.has(id)) continue
        generation.pending.delete(id)
        if (this.owners(id).size) ids.push(id)
        if (ids.length === 200) break
      }
      if (!ids.length) return
      const batch: Batch = { ids, owners: new Map(ids.map(id => [id, this.owners(id)])), controller: new AbortController() }
      generation.batches.add(batch)
      ids.forEach(id => generation.inflight.set(id, batch))
      void this.fetchSnapshots(ids, batch.controller.signal).then(result => {
        if (this.generation !== generation || generation.signal.aborted || batch.controller.signal.aborted) return
        for (const item of result.items) this.update(titleFromSnapshot(item))
        for (const id of result.missing_ids) {
          const owners = batch.owners.get(id)
          for (const [scope, token] of owners ?? []) {
            if (!this.scopes.has(scope) || scope.members.get(id) !== token) continue
            scope.members.delete(id)
            scope.handlers.missing(id)
          }
          // 新加入scope没有被这次missing确认；为它合并安排一次新检查。
          if (this.owners(id).size) generation.pending.add(id)
          else this.prune(id)
        }
      }).catch(() => {
        if (this.generation !== generation || generation.signal.aborted || batch.controller.signal.aborted) return
        for (const scope of this.scopes) if (ids.some(id => scope.members.has(id))) scope.handlers.error('标题同步失败，请重新加载会话')
      }).finally(() => {
        generation.batches.delete(batch)
        ids.forEach(id => { if (generation.inflight.get(id) === batch) generation.inflight.delete(id) })
        if (this.generation === generation) this.pump()
      })
    }
  }
}
