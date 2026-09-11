<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>总览</h1>
        <p>所有任务按统一目标间隔（{{ state.settings.interval_seconds }} 秒）观察；浏览器关闭时 VPS 上的采集仍在继续。</p>
      </div>
    </div>
    <div class="tiles">
      <div class="tile"><div class="label">在线实例</div><div class="value num">{{ online }} / {{ state.instances.length }}</div><div class="hint">单个实例失败不影响其他实例</div></div>
      <div class="tile"><div class="label">当前任务</div><div class="value num">{{ torrentTotal }}</div><div class="hint">包含暂停、零速和无变化任务</div></div>
      <div class="tile"><div class="label">当前上传 / 下载</div><div class="value num">{{ fmtSpeed(totalUp) }}</div><div class="hint">↓ {{ fmtSpeed(totalDown) }} · 各实例当前值合计</div></div>
      <div class="tile"><div class="label">数据目录</div><div class="value num">{{ fmtBytes(state.storage.directory_bytes) }}</div><div class="hint">{{ state.storage.paused ? '历史写入已暂停' : `预算 ${fmtBytes(state.storage.budget_bytes)} · 非硬配额` }}</div></div>
    </div>
    <v-card border class="card-pad mb-4">
      <div class="card-title">
        <h2>{{ selectedLabel }} 速度趋势</h2>
        <span class="sub">{{ state.selectedInstance === 'all' ? '按共同 UTC 桶对齐合计；缺少任一实例时留白，峰值不相加' : '该实例 qB 报告的全局速度' }}</span>
      </div>
      <HistoryChart :key="state.selectedInstance" :fetcher="fetchOverview" :ranges="ranges" default-range="1h" :allow-cumulative="state.selectedInstance !== 'all'" :retention-days="state.settings.retention_days" />
    </v-card>
    <div class="grid-2">
      <v-card border class="card-pad">
        <div class="card-title"><h2>实例状态</h2><span class="sub">最后成功时间按浏览器时区显示</span></div>
        <div v-if="!state.instances.length" class="text-medium-emphasis py-6 text-center">尚未添加 qB 连接。<router-link to="/connections">前往连接管理</router-link></div>
        <div v-for="i in state.instances" :key="i.instance.instance_id" class="d-flex ga-3 py-3" style="border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity))">
          <span class="dot mt-2" :class="connColor(i.connection_status)"></span>
          <div style="min-width: 0; flex: 1">
            <div class="d-flex align-center ga-2 flex-wrap">
              <b class="truncate">{{ i.instance.name }}</b>
              <v-chip size="x-small" variant="tonal">{{ connText(i.connection_status) }}</v-chip>
              <span class="text-caption" style="opacity: .7">{{ i.qb_version || 'qB 版本未知' }} · WebAPI {{ i.webapi_version || '未知' }}</span>
            </div>
            <div class="text-caption mt-1" style="opacity: .75">
              {{ i.torrent_count }} 个任务 · ↑{{ fmtSpeed(i.global?.up) }} ↓{{ fmtSpeed(i.global?.down) }} · 累计 ↑{{ fmtBytes(i.global?.uploaded) }} ↓{{ fmtBytes(i.global?.downloaded) }}
            </div>
            <div class="text-caption" style="opacity: .65">
              <template v-if="i.error">{{ i.error }}</template>
              <template v-else>目标 {{ i.target_seconds }}s · 实际 {{ i.actual_interval_ms ? (i.actual_interval_ms / 1000).toFixed(1) + 's' : '待采样' }} · 最近成功 {{ fmtAgo(i.last_success_at) }} · 10 分钟覆盖 {{ fmtPct(i.coverage_10m) }}</template>
            </div>
          </div>
        </div>
      </v-card>
      <v-card border class="card-pad">
        <div class="card-title"><h2>最近有上传的任务</h2><router-link to="/torrents" class="text-caption">查看全部 →</router-link></div>
        <div v-if="!recent.length" class="text-medium-emphasis py-6 text-center">健康采集后，任务会出现在这里。</div>
        <router-link v-for="t in recent" :key="t.torrent_id" :to="`/torrents/${t.torrent_id}`" class="d-flex align-center ga-3 py-2 text-decoration-none" style="color: inherit; border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity))">
          <v-chip size="x-small" variant="tonal" :color="stateColor(t.state)">{{ stateText(t.state) }}</v-chip>
          <span class="truncate" style="flex: 1" :title="t.name">{{ t.name || '未命名' }}</span>
          <span class="mono num">↑{{ fmtSpeed(t.current?.up) }}</span>
        </router-link>
      </v-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type SeriesResult } from '../api'
import { state, retentionRanges, type Torrent } from '../store'
import { fmtBytes, fmtSpeed, fmtPct, fmtAgo, connColor, connText, stateText, stateColor } from '../format'
import HistoryChart from '../components/HistoryChart.vue'

const recent = ref<Torrent[]>([])
const torrentTotal = ref(0)
const ranges = computed(() => retentionRanges())
const visible = computed(() => (state.selectedInstance === 'all' ? state.instances : state.instances.filter((i) => i.instance.instance_id === state.selectedInstance)))
const online = computed(() => state.instances.filter((i) => i.connection_status === 'online').length)
const totalUp = computed(() => visible.value.reduce((n, i) => n + Number(i.global?.up || 0), 0))
const totalDown = computed(() => visible.value.reduce((n, i) => n + Number(i.global?.down || 0), 0))
const selectedLabel = computed(() => (state.selectedInstance === 'all' ? '全部实例' : visible.value[0]?.instance.name || ''))

function fetchOverview(start: number, end: number, metric: string, counter: string, maxPoints: number, signal: AbortSignal): Promise<SeriesResult> {
  if (state.selectedInstance === 'all') return api(`/overview/series?start=${start}&end=${end}&metric=speed&max_points=${maxPoints}`, {}, { signal })
  return api(`/instances/${state.selectedInstance}/series?start=${start}&end=${end}&metric=${metric}&counter=${counter}&max_points=${maxPoints}`, {}, { signal })
}
async function loadRecent() {
  try {
    const q = state.selectedInstance === 'all' ? '' : `&instance=${state.selectedInstance}`
    const r = await api(`/torrents?limit=8&sort=last_upload&order=desc${q}`)
    recent.value = (r.items || []).filter((t: Torrent) => t.last_upload_at > 0)
    torrentTotal.value = r.total
  } catch {}
}
let t: number
onMounted(() => {
  loadRecent()
  t = window.setInterval(loadRecent, 10000)
})
onUnmounted(() => window.clearInterval(t))
watch(() => state.selectedInstance, loadRecent)
</script>
