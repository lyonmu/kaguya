import { get } from '../../api/http'
import type { FileContent, FileDiff, GitStatus, ProjectTree } from './types'

const PATH = '/v1/project'

export const fetchProjectTree = (id: string, signal?: AbortSignal) => get<ProjectTree>(`${PATH}/${encodeURIComponent(id)}/tree`, undefined, signal)
export const fetchFileContent = (id: string, path: string, signal?: AbortSignal) => get<FileContent>(`${PATH}/${encodeURIComponent(id)}/content`, { path }, signal)
export const fetchGitStatus = (id: string, signal?: AbortSignal) => get<GitStatus>(`${PATH}/${encodeURIComponent(id)}/git/status`, undefined, signal)
export const fetchFileDiff = (id: string, path: string, signal?: AbortSignal) => get<FileDiff>(`${PATH}/${encodeURIComponent(id)}/git/diff`, { path }, signal)
