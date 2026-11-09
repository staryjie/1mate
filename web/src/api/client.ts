let csrfToken = ''

export function setCSRFToken(token: string): void {
  csrfToken = token
}

export function getCSRFToken(): string {
  return csrfToken
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details?: unknown

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
  }
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
  raw?: boolean
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? 'GET'
  const headers: Record<string, string> = {}
  let body: BodyInit | undefined

  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(opts.body)
  }
  if (method !== 'GET' && method !== 'HEAD' && csrfToken) {
    headers['X-CSRF-Token'] = csrfToken
  }

  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      body,
      credentials: 'same-origin',
      signal: opts.signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      throw err
    }
    throw new ApiError(0, 'network_error', '无法连接到服务器，请检查网络后重试')
  }

  if (res.status === 204) {
    return undefined as T
  }

  const contentType = res.headers.get('content-type') ?? ''
  if (!res.ok) {
    let code = 'http_error'
    let message = `请求失败（HTTP ${res.status}）`
    let details: unknown
    if (contentType.includes('application/json')) {
      try {
        const payload = (await res.json()) as { code?: string; message?: string; details?: unknown }
        code = payload.code ?? code
        message = payload.message ?? message
        details = payload.details
      } catch {
        /* keep the generic message */
      }
    }
    throw new ApiError(res.status, code, message, details)
  }

  if (opts.raw) {
    return res as unknown as T
  }
  if (contentType.includes('application/json')) {
    return (await res.json()) as T
  }
  return (await res.text()) as unknown as T
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body: body ?? {} }),
  patch: <T>(path: string, body: unknown) => request<T>(path, { method: 'PATCH', body }),
  del: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}

export async function uploadFile<T>(path: string, file: File, fields: Record<string, string> = {}): Promise<T> {
  const form = new FormData()
  form.append('file', file)
  for (const [k, v] of Object.entries(fields)) {
    form.append(k, v)
  }
  const headers: Record<string, string> = {}
  if (csrfToken) {
    headers['X-CSRF-Token'] = csrfToken
  }
  const res = await fetch(path, { method: 'POST', body: form, headers, credentials: 'same-origin' })
  if (!res.ok) {
    let message = `上传失败（HTTP ${res.status}）`
    let code = 'upload_failed'
    try {
      const payload = (await res.json()) as { code?: string; message?: string }
      code = payload.code ?? code
      message = payload.message ?? message
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, code, message)
  }
  return (await res.json()) as T
}

/**
 * Opens the Server-Sent Events stream for a job. The returned function closes
 * the stream; onClose fires exactly once when the server signals completion.
 */
export function openJobStream(
  jobId: string,
  handlers: { onLine: (text: string) => void; onDone: (job: unknown) => void; onError?: () => void },
): () => void {
  const controller = new AbortController()
  let closed = false

  const close = () => {
    if (!closed) {
      closed = true
      controller.abort()
    }
  }

  void (async () => {
    try {
      const res = await fetch(`/api/v1/jobs/${jobId}/events`, {
        headers: { Accept: 'text/event-stream' },
        credentials: 'same-origin',
        signal: controller.signal,
      })
      if (!res.ok || !res.body) {
        handlers.onError?.()
        return
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let eventName = 'message'

      while (!closed) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        let idx = buffer.indexOf('\n\n')
        while (idx >= 0) {
          const chunk = buffer.slice(0, idx)
          buffer = buffer.slice(idx + 2)
          let data = ''
          for (const line of chunk.split('\n')) {
            if (line.startsWith('event:')) {
              eventName = line.slice(6).trim()
            } else if (line.startsWith('data:')) {
              data += line.slice(5).trimStart()
            }
          }
          if (data) {
            if (eventName === 'done') {
              try {
                handlers.onDone(JSON.parse(data))
              } catch {
                handlers.onDone(null)
              }
              close()
              return
            }
            try {
              const payload = JSON.parse(data) as { text?: string }
              if (payload.text) handlers.onLine(payload.text)
            } catch {
              handlers.onLine(data)
            }
          }
          eventName = 'message'
          idx = buffer.indexOf('\n\n')
        }
      }
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) {
        handlers.onError?.()
      }
    }
  })()

  return close
}
