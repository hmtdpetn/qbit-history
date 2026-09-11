<template>
  <div class="page">
    <v-btn variant="text" size="small" :prepend-icon="mdiArrowLeft" class="mb-2" @click="back">返回列表</v-btn>
    <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mb-3">{{ error }}</v-alert>
    <template v-if="t">
      <div class="page-head">
        <div style="min-width: 0; flex: 1">
          <h1 class="truncate" :title="t.name" style="max-width: 100%">{{ t.name || '（名称未知）' }}</h1>
          <p class="d-flex flex-wrap align-center ga-2 mt-2">
            <v-chip size="x-small" variant="tonal">{{ instanceName(t.instance_id) }}</v-chip>
            <v-chip size="x-small" variant="tonal" :color="stateColor(t.state)">{{ stateText(t.state) }}</v-chip>
            <v-chip v-if="t.category" size="x-small" variant="tonal">{{ t.category }}</v-chip>
            <v-chip v-for="g in tagList" :key="g" size="x-small" variant="outlined">{{ g }}</v-chip>
            <v-chip v-if="t.deletion_candidate" size="x-small" color="warning" variant="tonal">正在确认移除</v-chip>
            <span class="text-caption">大小 {{ fmtBytes(t.size) }} · 进度 {{ fmtPct(t.progress, 1) }} · 首次观察 {{ fmtDateTimeFull(t.first_seen_at) }} · 第 {{ t.generation }} 代</span>
          </p>
        </div>
        <v-btn size="small" variant="outlined" :prepend-icon="mdiContentCopy" @click="copyName">复制完整名称</v-btn>
      </div>
      <div class="tiles">
        <div class="tile"><div class="label">当前上传</div><div class="value num">{{ fmtSpeed(t.current?.up) }}</div><div class="hint">观察于 {{ fmtAgo(t.current?.at, tick) }}</div></div>
        <div class="tile"><div class="label">当前下载</div><div class="value num">{{ fmtSpeed(t.current?.down) }}</div><div class="hint">peers {{ t.peers }} · seeds {{ t.seeds }}</div></div>
        <div class="tile"><div class="label">累计上传（qB 报告）</div><div class="value num">{{ fmtBytes(t.current?.uploaded) }}</div><div class="hint">分享率 {{ t.ratio?.toFixed(2) ?? '—' }}</div></div>
        <div class="tile"><div class="label">累计下载（qB 报告）</div><div class="value num">{{ fmtBytes(t.current?.downloaded) }}</div><div class="hint">1h 有效上传 {{ t.upload_1h === null ? '统计中' : fmtBytes(t.upload_1h) }} · 24h {{ t.upload_24h === null ? '统计中' : fmtBytes(t.upload_24h) }}</div></div>
      </div>
      <v-card border class="card-pad">
        <div class="card-title"><h2>历史</h2><span class="sub">最近 24 小时逐秒无损；24–72 小时 60 秒摘要；更早 5 分钟摘要</span></div>
        <HistoryChart :fetcher="fetchSeries" :ranges="ranges" default-range="24h" tall :retention-days="state.settings.retention_days" />
      </v-card>
      <v-card border class="card-pad mt-4">
        <div class="card-title"><h2>任务信息</h2><span class="sub">元数据只在变化时记录；不保存 tracker、magnet 或路径</span></div>
        <div class="kv">
          <div><div class="k">qB key</div><div class="v mono">{{ t.qb_key }}</div></div>
          <div><div class="k">添加时间</div><div class="v">{{ t.added_on ? fmtDateTimeFull(t.added_on * 1000) : '—' }}</div></div>
          <div><div class="k">完成时间</div><div class="v">{{ t.completion_on > 0 ? fmtDateTimeFull(t.completion_on * 1000) : '—' }}</div></div>
          <div><div class="k">最近观察到上传</div><div class="v">{{ t.last_upload_at ? fmtDateTimeFull(t.last_upload_at) : '未观察到' }}</div></div>
          <div><div class="k">可用性</div><div class="v">{{ t.availability?.toFixed(2) ?? '—' }}</div></div>
          <div><div class="k">统计覆盖（24h）</div><div class="v">{{ fmtPct(t.stats_coverage) }}</div></div>
        </div>
      </v-card>
    </template>
    <div v-else-if="!error" class="text-center py-12"><v-progress-circular indeterminate /></div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { mdiArrowLeft, mdiContentCopy } from '@mdi/js'
import { api, type SeriesResult } from '../api'
import { state, instanceName, retentionRanges, type Torrent } from '../store'
import { fmtBytes, fmtSpeed, fmtPct, fmtAgo, fmtDateTimeFull, stateText, stateColor } from '../format'
import HistoryChart from '../components/HistoryChart.vue'

const route = useRoute()
const router = useRouter()
const t = ref<Torrent | null>(null)
const error = ref('')
const tick = ref(Date.now())
const ranges = computed(() => retentionRanges())
const tagList = computed(() => (t.value?.tags || '').split(',').map((s) => s.trim()).filter(Boolean))

async function load() {
  try {
    if (route.name === 'torrent-by-key') {
      const x = await api(`/instances/${route.params.id}/torrent-by-key/${encodeURIComponent(String(route.params.key))}`)
      router.replace(`/torrents/${x.torrent_id}`)
      return
    }
    t.value = await api(`/torrents/${route.params.id}`)
    error.value = ''
  } catch (e: any) {
    error.value = e.status === 404 ? '任务不存在或已从本应用移除。' : e.message
  }
}
function fetchSeries(start: number, end: number, metric: string, counter: string, maxPoints: number, signal: AbortSignal): Promise<SeriesResult> {
  return api(`/torrents/${route.params.id}/series?start=${start}&end=${end}&metric=${metric}&counter=${counter}&max_points=${maxPoints}`, {}, { signal })
}
function back() {
  history.length > 1 ? router.back() : router.push('/torrents')
}
async function copyName() {
  try {
    await navigator.clipboard.writeText(t.value?.name || '')
    state.notice = '已复制名称'
  } catch {
    state.notice = '浏览器拒绝了剪贴板访问'
  }
}
let timer: number
onMounted(() => {
  load()
  timer = window.setInterval(() => {
    tick.value = Date.now()
    if (t.value) load()
  }, 3000)
})
onUnmounted(() => window.clearInterval(timer))
watch(() => route.fullPath, load)
</script>
