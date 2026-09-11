import { reactive } from 'vue'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export const session = reactive({ authenticated: false, user: '', csrf: '', checked: false, version: '' })

export async function api<T = any>(path: string, init: RequestInit = {}, opts: { signal?: AbortSignal; headers?: Record<string, string> } = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body) headers.set('Content-Type', 'application/json')
  if (session.csrf) headers.set('X-CSRF-Token', session.csrf)
  for (const [k, v] of Object.entries(opts.headers || {})) headers.set(k, v)
  const res = await fetch('/api/v1' + path, { ...init, headers, signal: opts.signal, credentials: 'same-origin' })
  const body = await res.json().catch(() => ({}))
  if (res.status === 401) {
    session.authenticated = false
    session.csrf = ''
    throw new ApiError(401, '登录已失效，请重新登录')
  }
  if (!res.ok) throw new ApiError(res.status, body.error || `HTTP ${res.status}`)
  return body as T
}

export async function checkSession(): Promise<boolean> {
  try {
    const res = await fetch('/api/v1/auth/session', { credentials: 'same-origin' })
    session.checked = true
    if (!res.ok) {
      session.authenticated = false
      return false
    }
    const b = await res.json()
    session.authenticated = true
    session.user = b.user
    session.csrf = b.csrf
    session.version = b.version
    return true
  } catch {
    session.checked = true
    return false
  }
}

export async function login(name: string, password: string) {
  const res = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, password }), credentials: 'same-origin' })
  const b = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, b.error || '登录失败')
  await checkSession()
}

export async function logout() {
  try {
    await api('/auth/logout', { method: 'POST' })
  } catch {}
  session.authenticated = false
  session.csrf = ''
}

export const RANGE_MS: Record<string, number> = {
  '15m': 900e3, '30m': 1800e3, '1h': 3600e3, '2h': 7200e3, '6h': 21600e3, '12h': 43200e3, '24h': 86400e3, '3d': 259200e3, '7d': 604800e3, '14d': 1209600e3, '30d': 2592000e3
}

export interface Point { at: number; value: string | null; kind: string; source: string; resolution: number; quality: number; approximate: boolean; epoch: number }
export interface Curve { points: Point[]; extremes: Point[]; thinned: boolean; flattened: boolean }
export interface SeriesResult {
  requested_start: number; requested_end: number; effective_start: number; effective_end: number; actual_start: number; actual_end: number
  metric: string; counter_mode: string; up: Curve; down: Curve
  segments: { start: number; end: number; source: string; resolution: number; boundary_estimate: boolean }[]
  coverage: number; counter_coverage: number; observed_upload_bytes: string; observed_download_bytes: string
  unpersisted_tail: boolean; persisted_watermark: number
  gaps: { instance_id: string; series_id: number; at: number; end: number; kind: string; detail: string }[]
  notes: string[]; composition: string[]; peak_semantics: string; missing_components: boolean; step_ms: number
}
