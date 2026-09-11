import { reactive, watch } from 'vue'
import { api, session } from './api'

export interface Instance {
  instance: { instance_id: string; name: string; base_url: string; username: string; poll_enabled: boolean; credentials_saved: boolean }
  connection_status: string; error: string; qb_version: string; webapi_version: string; last_success_at: number; actual_interval_ms: number; request_ms: number; delay_ms: number
  target_seconds: number; rounds: number; skipped: number; failed_rounds: number; logical_samples: number; torrent_count: number; deletion_candidates: number
  bulk_delete_protection: boolean; protected_until: number; cooldown_until: number; global: { up: string; down: string; uploaded: string; downloaded: string; valid: number; at: number }; global_series_id: number; coverage_10m: number
}
export interface Torrent {
  torrent_id: number; instance_id: string; qb_key: string; generation: number; name: string; category: string; tags: string; size: string; state: string; progress: number; ratio: number; peers: number; seeds: number; availability: number
  added_on: number; completion_on: number; first_seen_at: number; series_id: number
  current: { at: number; up: string; down: string; uploaded: string; downloaded: string; valid: number; quality: number }
  deletion_candidate: boolean; last_upload_at: number; upload_1h: string | null; upload_24h: string | null; stats_coverage: number; stats_at: number
}
export interface Settings { retention_days: number; interval_seconds: number; data_budget_bytes: number; timeout_ms: number; response_limit_bytes: number }

export const state = reactive({
  instances: [] as Instance[],
  selectedInstance: 'all',
  settings: { retention_days: 7, interval_seconds: 1, data_budget_bytes: 2147483648, timeout_ms: 2000, response_limit_bytes: 8388608 } as Settings,
  storage: {} as Record<string, any>,
  managerError: '',
  loadedAt: 0,
  error: '',
  notice: ''
})

let timer: number | undefined

export async function refreshStatus() {
  if (!session.authenticated) return
  try {
    const s = await api('/status')
    state.instances = s.instances || []
    state.storage = s.storage || {}
    state.settings = s.settings || state.settings
    state.managerError = s.manager_error || ''
    state.loadedAt = Date.now()
    state.error = ''
    if (state.selectedInstance !== 'all' && !state.instances.some((i) => i.instance.instance_id === state.selectedInstance)) state.selectedInstance = 'all'
  } catch (e: any) {
    state.error = e.message
  }
}

export function startPolling(intervalMs = 5000) {
  stopPolling()
  refreshStatus()
  timer = window.setInterval(refreshStatus, intervalMs)
}
export function stopPolling() {
  if (timer) window.clearInterval(timer)
  timer = undefined
}
watch(() => session.authenticated, (v) => (v ? startPolling() : stopPolling()))

export function instanceName(id: string): string {
  return state.instances.find((i) => i.instance.instance_id === id)?.instance.name || '—'
}
export function instanceOf(id: string): Instance | undefined {
  return state.instances.find((i) => i.instance.instance_id === id)
}
export function retentionRanges(): string[] {
  const base = ['15m', '30m', '1h', '2h', '6h', '12h', '24h', '3d', '7d']
  if (state.settings.retention_days >= 14) base.push('14d')
  if (state.settings.retention_days >= 30) base.push('30d')
  return base
}
