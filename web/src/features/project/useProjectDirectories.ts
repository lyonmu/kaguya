import { useEffect, useRef, useState } from 'react'
import { fetchDirectories } from './api'
import type { Directories } from './types'

// 目录请求独立于项目列表分页；关闭或切换目录后，迟到响应不能返回给表单。
export function useProjectDirectories() {
  const [directories, setDirectories] = useState<Directories>()
  const [browsing, setBrowsing] = useState(false)
  const [directoryError, setDirectoryError] = useState('')
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => { request.current?.abort() }, [])
  const cancel = () => {
    request.current?.abort()
    request.current = null
    setBrowsing(false)
    setDirectories(undefined)
    setDirectoryError('')
  }
  const browse = async (path = '') => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setBrowsing(true)
    setDirectoryError('')
    setDirectories(undefined)
    try {
      const result = await fetchDirectories(path, controller.signal)
      if (controller.signal.aborted || request.current !== controller) return
      setDirectories(result)
      return result
    } catch (error) {
      if (!controller.signal.aborted && request.current === controller) setDirectoryError(error instanceof Error ? error.message : '项目操作失败')
    } finally {
      if (!controller.signal.aborted && request.current === controller) { request.current = null; setBrowsing(false) }
    }
  }
  return { directories, browsing, directoryError, browse, cancel }
}
