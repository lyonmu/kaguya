import { useContext, useState } from 'react'
import { App } from 'antd'
import { deleteConversation, updateConversation, upgradeConversationInstructions } from './api'
import type { useChat } from './useChat'
import type { useConversations } from './useConversations'
import { TitleSyncContext } from './titleSyncContext'
import { titleFromConversation } from './titleMerge'
import { isAbortError } from '../../api/errors'

interface Options {
  chat: ReturnType<typeof useChat>
  sessions: ReturnType<typeof useConversations>
  onProjectsChanged: () => void
}

/**
 * useConversationActions 集中会话写操作：改名/收藏/记忆模式/权限模式、删除与
 * 指令升级。它只调用 chat/sessions 暴露的操作，不复制会话状态。
 */
export function useConversationActions({ chat, sessions, onProjectsChanged }: Options) {
  const { message, modal } = App.useApp()
  const [saving, setSaving] = useState(false)
  const titles = useContext(TitleSyncContext)

  const update = async (payload: { title?: string; favorite?: boolean; memory_mode?: string; permission_mode?: string }) => {
    const id = chat.id
    if (payload.title !== undefined) chat.cancelTitleWait()
    setSaving(true)
    try {
      const result = await updateConversation(id, payload)
      if (result.id !== id) throw new Error('会话身份不匹配')
      chat.setConversation(result)
      titles?.update(titleFromConversation(result))
      await sessions.refreshQuietly()
      onProjectsChanged()
      return true
    } catch (error) {
      void message.error(error instanceof Error ? error.message : '更新失败')
      return false
    } finally {
      setSaving(false)
    }
  }

  const remove = () => {
    const id = chat.id
    return modal.confirm({
    title: '删除此对话？', content: '删除后无法恢复，也无法继续对话。', okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      chat.cancelTitleWait()
      setSaving(true)
      try {
        await deleteConversation(id)
        titles?.remove(id)
        if (!titles) { chat.forget(); await chat.select('') }
        sessions.refresh()
        onProjectsChanged()
      } catch (error) {
        void message.error(error instanceof Error ? error.message : '删除失败')
        throw error
      } finally {
        setSaving(false)
      }
    },
    })
  }

  const generateTitle = async () => {
    setSaving(true)
    try { await chat.generateTitle() }
    catch (error) { if (!isAbortError(error)) void message.error(error instanceof Error ? error.message : '生成标题失败') }
    finally { setSaving(false) }
  }

  const upgradeInstructions = async () => {
    if (!chat.id || chat.streaming) return
    setSaving(true)
    try {
      const result = await upgradeConversationInstructions(chat.id)
      chat.setConversation(result)
      void message.success('旧指令快照已归档，下一轮使用动态全局指令')
    } catch (err) {
      void message.error(err instanceof Error ? err.message : '指令升级失败')
    } finally {
      setSaving(false)
    }
  }

  return { saving, update, remove, upgradeInstructions, generateTitle }
}
