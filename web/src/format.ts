import { reactive } from 'vue'

export const prefs = reactive({
  theme: (localStorage.getItem('qh.theme') as 'light' | 'dark' | 'system') || 'system',
  utc: localStorage.getItem('qh.utc') === '1'
})
export function setTheme(t: 'light' | 'dark' | 'system') {
  prefs.theme = t
  localStorage.setItem('qh.theme', t)
}
export function setUTC(v: boolean) {
  prefs.utc = v
  localStorage.setItem('qh.utc', v ? '1' : '0')
}
export const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local'

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

export function fmtBytes(n: unknown, digits?: number): string {
  if (n === undefined || n === null || n === '') return '未知'
  let v = typeof n === 'string' ? Number(n) : (n as number)
  if (!Number.isFinite(v)) return '未知'
  let i = 0
  const neg = v < 0
  v = Math.abs(v)
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  const d = digits ?? (i === 0 ? 0 : v >= 100 ? 1 : 2)
  return (neg ? '-' : '') + v.toFixed(d) + ' ' + UNITS[i]
}
export function fmtSpeed(n: unknown): string {
  if (n === undefined || n === null) return '未知'
  const s = fmtBytes(n)
  return s === '未知' ? s : s + '/s'
}
export function fmtPct(v: number | undefined | null, digits = 0): string {
  if (v === undefined || v === null || !Number.isFinite(v)) return '未知'
  return (v * 100).toFixed(digits) + '%'
}
export function fmtTime(ms: number | undefined | null, opts: { seconds?: boolean; date?: boolean } = { seconds: true, date: true }): string {
  if (!ms) return '—'
  const d = new Date(ms)
  const o: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false }
  if (opts.seconds !== false) o.second = '2-digit'
  if (opts.date !== false) {
    o.month = '2-digit'
    o.day = '2-digit'
  }
  if (prefs.utc) o.timeZone = 'UTC'
  return new Intl.DateTimeFormat('zh-CN', o).format(d)
}
export function fmtDateTimeFull(ms: number | undefined | null): string {
  if (!ms) return '—'
  const d = new Date(ms)
  const o: Intl.DateTimeFormatOptions = { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }
  if (prefs.utc) o.timeZone = 'UTC'
  return new Intl.DateTimeFormat('zh-CN', o).format(d) + (prefs.utc ? ' UTC' : '')
}
export function fmtAgo(ms: number | undefined | null, now = Date.now()): string {
  if (!ms) return '从未'
  const s = Math.max(0, Math.round((now - ms) / 1000))
  if (s < 5) return '刚刚'
  if (s < 60) return `${s} 秒前`
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  return `${Math.floor(s / 86400)} 天前`
}
export function fmtDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} 秒`
  if (s < 3600) return `${(s / 60).toFixed(s % 60 ? 1 : 0)} 分钟`
  if (s < 86400) return `${(s / 3600).toFixed(1)} 小时`
  return `${(s / 86400).toFixed(1)} 天`
}
export function stateText(s: string | undefined): string {
  const m: Record<string, string> = {
    uploading: '做种中', stalledUP: '做种（无连接）', queuedUP: '排队做种', pausedUP: '已暂停（完成）', stoppedUP: '已停止（完成）', forcedUP: '强制做种', checkingUP: '校验中',
    downloading: '下载中', stalledDL: '下载（无连接）', queuedDL: '排队下载', pausedDL: '已暂停', stoppedDL: '已停止', forcedDL: '强制下载', metaDL: '获取元数据', checkingDL: '校验中', allocating: '分配空间', moving: '移动中', missingFiles: '文件缺失', error: '错误', checkingResumeData: '校验恢复数据', unknown: '未知'
  }
  return m[s || ''] || s || '未知'
}
export function stateColor(s: string | undefined): string {
  if (!s) return 'default'
  if (s.startsWith('paused') || s.startsWith('stopped')) return 'default'
  if (s === 'error' || s === 'missingFiles') return 'error'
  if (s.includes('DL') || s === 'downloading' || s === 'metaDL') return 'info'
  if (s.startsWith('stalled') || s.startsWith('queued')) return 'warning'
  return 'success'
}
export function connText(s: string | undefined): string {
  const m: Record<string, string> = { online: '在线', offline: '离线', connecting: '连接中', authentication_cooldown: '认证冷却', monitoring_stopped: '已停止采集', credential_error: '凭据不可用', invalid_address: '地址无效' }
  return m[s || ''] || s || '未知'
}
export function connColor(s: string | undefined): string {
  return s === 'online' ? 'good' : s === 'connecting' ? 'warn' : s === 'monitoring_stopped' ? 'idle' : 'bad'
}
