export type GitFileStatus = 'modified' | 'added' | 'deleted' | 'renamed' | 'copied' | 'untracked' | 'conflicted'

export interface GitFile {
  path: string
  status: GitFileStatus
  staged: boolean
  additions: number
  deletions: number
}

export interface GitStatus {
  is_git: boolean
  message?: string
  branch: string
  head: string
  files: GitFile[]
  truncated: boolean
}

export interface FileDiff {
  path: string
  binary: boolean
  truncated: boolean
  diff: string
}

export interface ProjectNode {
  name: string
  path: string
  is_dir: boolean
  size: number
  children?: ProjectNode[]
}

export interface ProjectTree {
  name: string
  path: string
  items: ProjectNode[]
  truncated: boolean
}

export interface FileContent {
  path: string
  size: number
  binary: boolean
  truncated: boolean
  content: string
}

export type CodeBrowseView = 'diff' | 'file'
