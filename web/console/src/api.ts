export class ApiError extends Error {
  status: number
  constructor(status: number, msg: string) {
    super(msg)
    this.status = status
  }
}

export type Row = Record<string, any>

export const msg = (e: unknown): string => (e instanceof Error ? e.message : String(e))

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    window.location.href = '/auth/login'
    throw new ApiError(401, 'sign-in required')
  }
  const text = await r.text()
  let data: any = text
  try {
    data = text ? JSON.parse(text) : {}
  } catch {
    /* plain text */
  }
  if (!r.ok) throw new ApiError(r.status, (data && data.error) || text || r.statusText)
  return data as T
}
