export interface ApiResponse<T> {
  code: number
  data?: T
  message?: string
}

type QueryValue = string | number | boolean | null | undefined

const SUCCESS_CODE = 100000
const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/kaguya/api').replace(
  /\/$/,
  '',
)

export class ApiRequestError extends Error {
  code?: number
  status?: number

  constructor(message: string, options?: { code?: number; status?: number }) {
    super(message)
    this.name = 'ApiRequestError'
    this.code = options?.code
    this.status = options?.status
  }
}

function buildUrl(path: string, query?: Record<string, QueryValue>) {
  const searchParams = new URLSearchParams()

  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') {
      searchParams.set(key, String(value))
    }
  })

  const queryString = searchParams.toString()
  return `${apiBaseUrl}${path}${queryString ? `?${queryString}` : ''}`
}

export async function get<T>(
  path: string,
  query?: Record<string, QueryValue>,
  signal?: AbortSignal,
): Promise<T> {
  let response: Response

  try {
    response = await fetch(buildUrl(path, query), {
      method: 'GET',
      headers: {
        Accept: 'application/json',
      },
      credentials: 'same-origin',
      signal,
    })
  } catch (requestError) {
    if (signal?.aborted) {
      throw requestError
    }

    throw new ApiRequestError('无法连接到服务端，请确认 Kaguya API 已启动')
  }

  let payload: ApiResponse<T>

  try {
    payload = (await response.json()) as ApiResponse<T>
  } catch {
    throw new ApiRequestError('服务端返回了无法解析的响应', {
      status: response.status,
    })
  }

  if (typeof payload !== 'object' || payload === null) {
    throw new ApiRequestError('服务端响应格式不正确', {
      status: response.status,
    })
  }

  if (!response.ok) {
    throw new ApiRequestError(payload.message || `请求失败（${response.status}）`, {
      code: payload.code,
      status: response.status,
    })
  }

  if (typeof payload.code !== 'number') {
    throw new ApiRequestError('服务端响应格式不正确', {
      status: response.status,
    })
  }

  if (payload.code !== SUCCESS_CODE) {
    throw new ApiRequestError(payload.message || '请求处理失败', {
      code: payload.code,
      status: response.status,
    })
  }

  return payload.data as T
}
