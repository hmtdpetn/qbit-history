<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>服务状态</h1>
        <p>用于判断采集质量、缺口和持久化保护。单个 qB 离线不会重启本应用；健康检查只看本服务自身。</p>
      </div>
      <div class="text-caption" style="opacity: .7">qbit-history v{{ session.version }} · SQLite {{ s.sqlite }} · 运行 {{ fmtDuration((status.uptime_s || 0) * 1000) }}</div>
    </div>
    <v-card border class="mb-4">
      <div class="card-pad pb-0"><div class="card-title"><h2>采集器</h2><span class="sub">每实例一个采集器、一个在途请求；跳过的轮次不会补造样本</span></div></div>
      <div class="table-wrap">
        <v-table density="comfortable">
          <thead><tr><th>实例</th><th>状态</th><th>版本</th><th>目标 / 实际间隔</th><th>请求耗时</th><th>轮次 / 跳过 / 失败</th><th>10 分钟覆盖</th><th>逻辑样本</th><th>任务</th><th>保护</th><th>最近成功</th></tr></thead>
          <tbody>
            <tr v-for="i in state.instances" :key="i.instance.instance_id">
              <td><div class="d-flex align-center ga-2"><span class="dot" :class="connColor(i.connection_status)"></span>{{ i.instance.name }}</div></td>
              <td>{{ connText(i.connection_status) }}<div v-if="i.error" class="text-caption text-error">{{ i.error }}</div><div v-if="i.cooldown_until" class="text-caption">冷却至 {{ fmtTime(i.cooldown_until) }}</div></td>
              <td class="text-caption">{{ i.qb_version || '—' }}<br />API {{ i.webapi_version || '—' }}</td>
              <td class="num">{{ i.target_seconds }}s / {{ i.actual_interval_ms ? (i.actual_interval_ms / 1000).toFixed(2) + 's' : '—' }}</td>
              <td class="num">{{ i.request_ms }} ms<div class="text-caption">调度延迟 {{ i.delay_ms }} ms</div></td>
              <td class="num">{{ i.rounds }} / {{ i.skipped }} / {{ i.failed_rounds }}</td>
              <td class="num">{{ fmtPct(i.coverage_10m) }}</td>
              <td class="num">{{ i.logical_samples.toLocaleString() }}</td>
              <td class="num">{{ i.torrent_count }}<span v-if="i.deletion_candidates" class="text-warning"> (−{{ i.deletion_candidates }} 待确认)</span></td>
              <td class="text-caption"><span v-if="i.bulk_delete_protection" class="text-warning">批量删除保护</span><span v-else-if="i.protected_until > now">启动保护至 {{ fmtTime(i.protected_until) }}</span><span v-else>—</span></td>
              <td class="text-caption">{{ fmtAgo(i.last_success_at, now) }}</td>
            </tr>
            <tr v-if="!state.instances.length"><td colspan="11" class="text-center text-medium-emphasis py-6">暂无采集器</td></tr>
          </tbody>
        </v-table>
      </div>
    </v-card>
    <div class="grid-2">
      <v-card border class="card-pad">
        <div class="card-title"><h2>持久化与安全边界</h2></div>
        <div class="d-flex ga-3 mb-3"><span class="dot mt-2" :class="s.paused ? 'bad' : 'good'"></span><div><b>{{ s.paused ? '历史写入已暂停' : '历史写入正常' }}</b><div class="text-caption" style="opacity: .7">{{ s.reason || '有界队列 · 30 秒批量提交 · 5 分钟封块 · 每小时摘要封块' }}</div><div class="text-caption" style="opacity: .7">最近提交 {{ fmtAgo(s.last_commit_at, now) }}（事务 {{ s.last_tx_ms }} ms）· WAL {{ fmtBytes(s.wal_bytes) }} · 队列 {{ fmtBytes(s.queue_bytes) }} · 已丢弃 {{ s.queue_dropped || 0 }}<span v-if="s.clock_hold" class="text-warning"> · 时钟前跳，保留清理已暂停</span></div></div></div>
        <div class="d-flex ga-3 mb-3"><span class="dot mt-2 good"></span><div><b>上游只读白名单</b><div class="text-caption mono" style="opacity: .8">{{ allowed }}</div><div class="text-caption" style="opacity: .7">没有任何 qB 任务控制、配置或 Tracker 接口；HTTP 重定向被拒绝；凭据用 AES-GCM 加密，主密钥单独挂载。</div></div></div>
        <div class="d-flex ga-3"><span class="dot mt-2 idle"></span><div><b>威胁边界</b><div class="text-caption" style="opacity: .7">应用持有的 qB 凭据本身不是 qB 侧的只读权限；只读由白名单、隔离和测试约束。容器被攻破时不保证绝对只读。</div></div></div>
      </v-card>
      <v-card border class="card-pad">
        <div class="card-title"><h2>有效载荷归属（前 30）</h2><span class="sub">共享索引/空闲页另计，无法精确平摊</span></div>
        <div class="table-wrap">
          <v-table density="compact">
            <thead><tr><th>实例</th><th>序列</th><th class="text-right">载荷</th></tr></thead>
            <tbody>
              <tr v-for="a in attributions" :key="a.series_id"><td>{{ instanceName(a.instance_id) }}</td><td class="num">{{ a.series_id }}<span v-if="isGlobal(a)" class="text-caption"> (全局)</span></td><td class="num text-right">{{ fmtBytes(a.payload_bytes) }}</td></tr>
              <tr v-if="!attributions.length"><td colspan="3" class="text-center text-medium-emphasis py-4">尚无数据</td></tr>
            </tbody>
          </v-table>
        </div>
      </v-card>
    </div>
    <v-card border class="card-pad mt-4">
      <div class="card-title"><h2>最近 24 小时事件</h2><span class="sub">缺口、计数重置、时钟异常、任务增删、连接变化</span></div>
      <div class="table-wrap">
        <v-table density="compact">
          <thead><tr><th>时间</th><th>实例</th><th>类型</th><th>说明</th></tr></thead>
          <tbody>
            <tr v-for="(e, i) in events" :key="i"><td class="text-caption num">{{ fmtDateTimeFull(e.at) }}<span v-if="e.end > e.at"> → {{ fmtTime(e.end) }}</span></td><td>{{ e.instance_id ? instanceName(e.instance_id) : '—' }}</td><td><v-chip size="x-small" variant="tonal" :color="eventColor(e.kind)">{{ e.kind }}</v-chip></td><td class="text-caption">{{ e.detail }}</td></tr>
            <tr v-if="!events.length"><td colspan="4" class="text-center text-medium-emphasis py-4">没有事件</td></tr>
          </tbody>
        </v-table>
      </div>
    </v-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, session } from '../api'
import { state, instanceName } from '../store'
import { fmtBytes, fmtPct, fmtAgo, fmtTime, fmtDateTimeFull, fmtDuration, connColor, connText } from '../format'

const status = ref<any>({})
const events = ref<any[]>([])
const now = ref(Date.now())
const s = computed(() => state.storage)
const allowed = computed(() => Object.entries(status.value.allowed_upstream || {}).map(([p, m]) => `${m} ${p}`).join(' · '))
const attributions = computed(() => [...(s.value.attributions || [])].sort((a: any, b: any) => b.payload_bytes - a.payload_bytes).slice(0, 30))
function isGlobal(a: any) {
  return state.instances.some((i) => i.global_series_id === a.series_id)
}
function eventColor(k: string) {
  return k.includes('gap') ? 'error' : k === 'counter_reset' || k === 'clock_change' ? 'warning' : k.startsWith('torrent') ? 'info' : 'default'
}
async function load() {
  now.value = Date.now()
  try {
    status.value = await api('/status')
    const r = await api('/events')
    events.value = r.events || []
  } catch {}
}
let t: number
onMounted(() => {
  load()
  t = window.setInterval(load, 10000)
})
onUnmounted(() => window.clearInterval(t))
</script>
