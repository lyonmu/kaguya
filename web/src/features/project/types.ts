/** 项目实体的网络边界类型；api.ts 临时 re-export 以保证旧导入继续可用。 */
export interface Project {
  id: string
  name: string
  path: string
  description: string
  created_at: string
}

export interface ProjectPage {
  items: Project[]
  total: number
  page: number
  page_size: number
}

export interface Directories {
  home: string
  path: string
  parent: string
  items: { name: string; path: string }[]
}

export type ProjectInput = Pick<Project, 'name' | 'path' | 'description'>

/** 引用候选：type 来自服务端 Stat，path 是工作区相对斜杠路径。 */
export interface ReferenceCandidate {
  type: 'file' | 'directory'
  path: string
  name?: string
}

/** GET project/:id/files 响应；旧客户端只读 files，新选择器读 items 与 cursor。 */
export interface ProjectFilesPage {
  files: string[]
  items?: ReferenceCandidate[]
  truncated: boolean
  next_cursor?: string
}
