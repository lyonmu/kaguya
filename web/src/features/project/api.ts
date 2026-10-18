import { del, get, post, put } from '../../api/http'
import { projectFilesGuard } from './guards'
import type { Directories, Project, ProjectFilesPage, ProjectInput, ProjectPage } from './types'

// 实体类型移至 types.ts；此处临时 re-export，避免一次迁移全部调用点。
export type { Directories, Project, ProjectInput, ProjectPage } from './types'

export const PATH = '/v1/project'
export const fetchProjects = (keyword: string, page: number, signal?: AbortSignal) => get<ProjectPage>(`${PATH}/page`, { keyword, page, page_size: 20 }, signal)
export const fetchProject = (id: string, signal?: AbortSignal) => get<Project>(`${PATH}/${encodeURIComponent(id)}`, undefined, signal)
export const saveProject = (id: string, data: ProjectInput, signal?: AbortSignal) => id ? put<Project>(`${PATH}/${encodeURIComponent(id)}`, data, signal) : post<Project>(PATH, data, signal)
export const deleteProject = (id: string, signal?: AbortSignal) => del(`${PATH}/${encodeURIComponent(id)}`, undefined, signal)
export const fetchDirectories = (path = '', signal?: AbortSignal) => get<Directories>(`${PATH}/directories`, { path }, signal)
// 引用候选：目录与文件统一协议；include_directories 只影响新 items，旧 files 字段保持兼容。
export const fetchProjectFiles = (id: string, query: string, options?: { includeDirectories?: boolean; cursor?: string; signal?: AbortSignal }) =>
  get<ProjectFilesPage>(`${PATH}/${encodeURIComponent(id)}/files`, { query, include_directories: options?.includeDirectories || undefined, cursor: options?.cursor }, options?.signal, projectFilesGuard)
