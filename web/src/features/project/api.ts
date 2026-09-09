import { del, get, post, put } from '../../api/http'

export interface Project { id: string; name: string; path: string; description: string; created_at: string }
export interface ProjectPage { items: Project[]; total: number; page: number; page_size: number }
export interface Directories { home: string; path: string; parent: string; items: { name: string; path: string }[] }
export type ProjectInput = Pick<Project, 'name' | 'path' | 'description'>
const PATH = '/v1/project'
export const fetchProjects = (keyword: string, page: number, signal?: AbortSignal) => get<ProjectPage>(`${PATH}/page`, { keyword, page, page_size: 20 }, signal)
export const fetchProject = (id: string) => get<Project>(`${PATH}/${encodeURIComponent(id)}`)
export const saveProject = (id: string, data: ProjectInput) => id ? put<Project>(`${PATH}/${encodeURIComponent(id)}`, data) : post<Project>(PATH, data)
export const deleteProject = (id: string) => del(`${PATH}/${encodeURIComponent(id)}`)
export const fetchDirectories = (path = '', signal?: AbortSignal) => get<Directories>(`${PATH}/directories`, { path }, signal)
