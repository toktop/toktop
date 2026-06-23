import type { ApiErrorBody } from "./types"

export class ApiError extends Error {
  code: string
  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}

type Params = Record<string, string | number | boolean | string[] | undefined>

function qs(params?: Params): string {
  if (!params) return ""
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue
    if (Array.isArray(v)) v.forEach((x) => sp.append(k, x))
    else sp.append(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ""
}

export async function apiGet<T>(path: string, params?: Params): Promise<T> {
  const res = await fetch(`/v1${path}${qs(params)}`, { credentials: "same-origin" })
  if (!res.ok) {
    let code = "http_error"
    let message = res.statusText
    try {
      const body = (await res.json()) as ApiErrorBody
      code    = body.error.code
      message = body.error.message
    } catch {
      // non-JSON error body; keep defaults
    }
    throw new ApiError(code, message)
  }
  return (await res.json()) as T
}
