<template>
  <div>
    <div class="range-bar mb-3">
      <v-btn-toggle v-if="allowCumulative" v-model="metric" mandatory density="compact" variant="outlined" divided color="primary">
        <v-btn value="speed" size="small">速度</v-btn>
        <v-btn value="cumulative" size="small">累计量</v-btn>
      </v-btn-toggle>
      <v-btn-toggle v-if="metric === 'cumulative'" v-model="counter" mandatory density="compact" variant="outlined" divided color="primary">
        <v-btn value="reported" size="small">报告计数</v-btn>
        <v-btn value="delta" size="small">窗口增量</v-btn>
      </v-btn-toggle>
      <v-btn-toggle v-model="range" density="compact" variant="outlined" divided color="primary" class="flex-wrap" @update:model-value="onPreset">
        <v-btn v-for="r in ranges" :key="r" :value="r" size="small">{{ r }}</v-btn>
      </v-btn-toggle>
      <v-menu v-model="customOpen" :close-on-content-click="false">
        <template #activator="{ props: p }">
          <v-btn v-bind="p" size="small" variant="outlined" :prepend-icon="mdiCalendarRange">自定义</v-btn>
        </template>
        <v-card class="pa-4" min-width="300">
          <v-text-field v-model="customStart" type="datetime-local" label="开始（本地时区）" step="1" density="compact" />
          <v-text-field v-model="customEnd" type="datetime-local" label="结束（本地时区）" step="1" density="compact" />
          <div class="d-flex justify-end ga-2"><v-btn size="small" variant="text" @click="customOpen = false">取消</v-btn><v-btn size="small" color="primary" @click="applyCustom">应用</v-btn></div>
        </v-card>
      </v-menu>
      <v-spacer />
      <v-btn size="small" variant="text" :prepend-icon="mdiMagnifyMinusOutline" @click="zoomOut">缩小</v-btn>
      <v-btn size="small" :variant="follow ? 'tonal' : 'outlined'" :color="follow ? 'primary' : undefined" :prepend-icon="follow ? mdiPlay : mdiPause" @click="toggleFollow">{{ follow ? '跟随实时' : '已暂停跟随' }}</v-btn>
      <v-btn size="small" variant="text" :prepend-icon="mdiRestore" @click="reset">回到最新</v-btn>
    </div>
    <div class="d-flex flex-wrap align-center ga-2 mb-2 text-caption" style="opacity: .8">
      <v-chip v-for="s in segmentChips" :key="s.key" :color="s.color" variant="tonal" size="x-small">{{ s.text }}</v-chip>
      <v-chip v-if="result?.unpersisted_tail" size="x-small" variant="tonal" color="info">含未持久化尾部</v-chip>
      <v-chip v-if="result && (result.up.thinned || result.down.thinned)" size="x-small" variant="tonal">显示抽稀（极值保留）</v-chip>
      <v-chip v-if="result && (result.up.flattened || result.down.flattened)" size="x-small" variant="tonal" color="secondary">趋势近似（5% 容差）</v-chip>
      <v-chip v-if="result?.missing_components" size="x-small" variant="tonal" color="warning">部分实例缺失，留白</v-chip>
      <span v-if="result">覆盖率 {{ fmtPct(result.coverage) }}</span>
      <span v-if="result && metric === 'cumulative'">窗口内已观察增量 ↑{{ fmtBytes(result.observed_upload_bytes) }} ↓{{ fmtBytes(result.observed_download_bytes) }}（计数覆盖 {{ fmtPct(result.counter_coverage) }}）</span>
      <v-spacer />
      <v-switch v-model="showExtremes" color="primary" density="compact" hide-details label="极值标记" class="ma-0" style="flex: none" />
      <v-switch v-model="showUp" color="upload" density="compact" hide-details label="上传" class="ma-0" style="flex: none" />
      <v-switch v-model="showDown" color="download" density="compact" hide-details label="下载" class="ma-0" style="flex: none" />
    </div>
    <div class="position-relative">
      <div ref="el" class="chart" :class="{ tall }" role="img" :aria-label="ariaLabel"></div>
      <v-progress-linear v-if="loading" indeterminate absolute location="top" height="2" />
      <div v-if="empty && !loading" class="position-absolute text-center text-medium-emphasis" style="inset: 0; display: grid; place-items: center; pointer-events: none">
        <div><div>该窗口内没有数据</div><div class="text-caption">{{ emptyHint }}</div></div>
      </div>
    </div>
    <div class="legend-note mt-2">
      {{ windowText }} · 时区 {{ prefs.utc ? 'UTC' : localZone }}。缺口断线；旧摘要显示加权均值与桶内极值；展平区间为 ±5% 趋势近似，不改变数据库数据。
      <span v-if="error" class="text-error">{{ error }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useTheme, useDisplay } from 'vuetify'
import * as echarts from 'echarts/core'
import { LineChart, ScatterChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, MarkAreaComponent, MarkLineComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { mdiCalendarRange, mdiMagnifyMinusOutline, mdiPlay, mdiPause, mdiRestore } from '@mdi/js'
import { RANGE_MS, type SeriesResult, type Point } from '../api'
import { fmtBytes, fmtSpeed, fmtPct, fmtDateTimeFull, fmtTime, prefs, localZone } from '../format'

echarts.use([LineChart, ScatterChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, MarkAreaComponent, MarkLineComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  fetcher: (start: number, end: number, metric: string, counter: string, maxPoints: number, signal: AbortSignal) => Promise<SeriesResult>
  ranges: string[]
  defaultRange?: string
  allowCumulative?: boolean
  tall?: boolean
  retentionDays?: number
}>(), { defaultRange: '1h', allowCumulative: true, tall: false, retentionDays: 7 })

const theme = useTheme()
const { width } = useDisplay()
const el = ref<HTMLElement | null>(null)
let chart: echarts.ECharts | undefined
const metric = ref<'speed' | 'cumulative'>('speed')
const counter = ref<'reported' | 'delta'>('reported')
const range = ref<string | undefined>(props.defaultRange)
const follow = ref(true)
const win = ref({ start: Date.now() - RANGE_MS[props.defaultRange], end: Date.now() })
const result = ref<SeriesResult | null>(null)
const loading = ref(false)
const error = ref('')
const showExtremes = ref(true)
const showUp = ref(true)
const showDown = ref(true)
const customOpen = ref(false)
const customStart = ref('')
const customEnd = ref('')
let seq = 0
let controller: AbortController | undefined
let debounce: number | undefined
let refresh: number | undefined
let ignoreZoom = false

const empty = computed(() => !!result.value && !result.value.up.points.some((p) => p.value !== null) && !result.value.down.points.some((p) => p.value !== null))
const emptyHint = computed(() => (result.value && result.value.effective_start >= result.value.effective_end ? '超出保留范围' : '可能是采集缺口、任务尚未观察到，或时间早于首次观察'))
const windowText = computed(() => `${fmtDateTimeFull(win.value.start)} → ${fmtDateTimeFull(win.value.end)}`)
const ariaLabel = computed(() => `${metric.value === 'speed' ? '速度' : '累计量'}历史图，${windowText.value}`)
const segmentChips = computed(() => {
  if (!result.value) return []
  return result.value.segments.map((s, i) => ({
    key: i,
    color: s.source === 'raw' ? 'success' : s.resolution === 60 ? 'info' : 'warning',
    text: s.source === 'raw' ? '原始逐秒样本' : `${s.resolution} 秒摘要${s.boundary_estimate ? '（边界估算）' : ''}`
  }))
})

function toLocalInput(ms: number) {
  const d = new Date(ms - new Date().getTimezoneOffset() * 60000)
  return d.toISOString().slice(0, 19)
}
function onPreset(r: string | undefined) {
  if (!r) return
  follow.value = true
  win.value = { start: Date.now() - RANGE_MS[r], end: Date.now() }
  load()
}
function applyCustom() {
  const a = new Date(customStart.value).getTime()
  const b = new Date(customEnd.value).getTime()
  if (!Number.isFinite(a) || !Number.isFinite(b) || b <= a) return
  customOpen.value = false
  range.value = undefined
  follow.value = false
  win.value = { start: a, end: Math.min(b, Date.now()) }
  load()
}
function toggleFollow() {
  follow.value = !follow.value
  if (follow.value) {
    const span = win.value.end - win.value.start
    win.value = { start: Date.now() - span, end: Date.now() }
    load()
  }
}
function reset() {
  range.value = props.defaultRange
  onPreset(props.defaultRange)
}
function zoomOut() {
  const span = win.value.end - win.value.start
  const center = (win.value.start + win.value.end) / 2
  let start = center - span
  let end = center + span
  if (end > Date.now()) {
    end = Date.now()
    start = end - span * 2
  }
  start = Math.max(start, Date.now() - props.retentionDays * 86400000 - span)
  follow.value = false
  range.value = undefined
  win.value = { start, end }
  load()
}
function maxPoints() {
  return width.value >= 1400 ? 4000 : 2000
}
async function load() {
  controller?.abort()
  controller = new AbortController()
  const my = ++seq
  loading.value = true
  error.value = ''
  try {
    const r = await props.fetcher(Math.floor(win.value.start), Math.floor(win.value.end), metric.value, counter.value, maxPoints(), controller.signal)
    if (my !== seq) return // an older response must never overwrite a newer window
    result.value = r
    draw()
  } catch (e: any) {
    if (e?.name === 'AbortError' || my !== seq) return
    error.value = e.message
  } finally {
    if (my === seq) loading.value = false
  }
  scheduleRefresh()
}
function scheduleRefresh() {
  if (refresh) window.clearTimeout(refresh)
  if (!follow.value) return
  const span = win.value.end - win.value.start
  const ms = span <= 3600e3 ? 5000 : span <= 86400e3 ? 30000 : 60000
  refresh = window.setTimeout(() => {
    if (!follow.value) return
    win.value = { start: Date.now() - span, end: Date.now() }
    load()
  }, ms)
}
function onZoom() {
  if (ignoreZoom || !chart) return
  if (debounce) window.clearTimeout(debounce)
  debounce = window.setTimeout(() => {
    const opt: any = chart!.getOption()
    const dz = opt.dataZoom?.[0]
    if (!dz) return
    const s = dz.startValue, e = dz.endValue
    if (typeof s !== 'number' || typeof e !== 'number' || e - s < 5000) return
    const span = win.value.end - win.value.start
    if (Math.abs(s - win.value.start) < span * 0.005 && Math.abs(e - win.value.end) < span * 0.005) return
    follow.value = false
    range.value = undefined
    win.value = { start: s, end: e }
    load()
  }, 260)
}
function kindText(k: string) {
  return ({ observation: '原始观测', weighted_mean: '桶加权均值', flat_5pct: '趋势近似（±5%）', counter_endpoint: '计数端点', minimum: '桶内最低', maximum: '桶内最高', aligned_sum: '对齐合计', gap: '缺口' } as Record<string, string>)[k] || k
}
function toData(points: Point[]) {
  return points.map((p) => [p.at, p.value === null ? null : Number(p.value), p.kind, p.resolution, p.approximate] as any)
}
function draw() {
  if (!el.value) return
  if (!chart) {
    chart = echarts.init(el.value)
    chart.on('datazoom', onZoom)
  }
  const r = result.value
  if (!r) return
  const dark = theme.global.current.value.dark
  const c = theme.global.current.value.colors
  const isSpeed = metric.value === 'speed'
  const fmt = isSpeed ? fmtSpeed : fmtBytes
  const gaps = r.gaps.filter((g) => ['sample_gap', 'summary_gap', 'storage_gap', 'gap'].includes(g.kind) && g.end > g.at)
  const lines = r.gaps.filter((g) => ['counter_reset', 'composition_change', 'clock_change', 'torrent_added', 'torrent_removed', 'address_change'].includes(g.kind))
  const mk = (name: string, color: string, points: Point[], show: boolean) => ({
    name, type: 'line', showSymbol: false, symbolSize: 4, connectNulls: false, smooth: false, sampling: undefined, animation: false, lineStyle: { width: 1.6, color }, itemStyle: { color }, emphasis: { disabled: true },
    data: show ? toData(points) : [], z: 3
  })
  const ext = (name: string, color: string, points: Point[], show: boolean) => ({
    name, type: 'scatter', symbolSize: 7, animation: false, itemStyle: { color, opacity: 0.85 }, z: 4,
    data: show ? points.map((p) => ({ value: [p.at, Number(p.value), p.kind, p.resolution], symbol: p.kind === 'maximum' ? 'triangle' : 'diamond' })) : []
  })
  const opt: echarts.EChartsCoreOption = {
    animation: false,
    backgroundColor: 'transparent',
    textStyle: { color: dark ? '#c9d1e0' : '#374151' },
    grid: { left: 64, right: 18, top: 28, bottom: 58, containLabel: false },
    legend: { top: 0, right: 8, icon: 'roundRect', itemWidth: 14, itemHeight: 6, textStyle: { color: dark ? '#c9d1e0' : '#374151' }, data: ['上传', '下载'] },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross', snap: true }, confine: true, transitionDuration: 0,
      backgroundColor: dark ? '#1f2633' : '#fff', borderColor: dark ? '#2e3746' : '#e5e7eb', textStyle: { color: dark ? '#e6edf7' : '#111827', fontSize: 12 },
      formatter: (ps: any[]) => {
        if (!ps?.length) return ''
        const at = ps[0].value[0]
        let html = `<div style="font-weight:600">${fmtDateTimeFull(at)}</div>`
        for (const p of ps) {
          const v = p.value[1]
          const kind = p.value[2]
          html += `<div>${p.marker} ${p.seriesName}: <b>${v === null || v === undefined ? '缺口' : fmt(v)}</b> <span style="opacity:.6">${kindText(kind)}${p.value[3] ? ` · ${p.value[3]}s` : ''}${p.value[4] ? ' · 估算' : ''}</span></div>`
        }
        return html
      }
    },
    xAxis: {
      type: 'time', min: win.value.start, max: win.value.end, axisLine: { lineStyle: { color: dark ? '#3a4356' : '#d1d5db' } }, splitLine: { show: false },
      axisLabel: { hideOverlap: true, color: dark ? '#9aa4b5' : '#6b7280', formatter: (v: number) => fmtTime(v, { seconds: win.value.end - win.value.start < 3 * 3600e3, date: win.value.end - win.value.start > 86400e3 }) }
    },
    // minInterval keeps an all-zero (or byte-sized) series from producing several identically rounded labels.
    yAxis: { type: 'value', min: 0, minInterval: 1024, axisLabel: { color: dark ? '#9aa4b5' : '#6b7280', formatter: (v: number) => fmt(v) }, splitLine: { lineStyle: { color: dark ? '#242c3a' : '#eef0f4' } } },
    dataZoom: [
      { type: 'inside', filterMode: 'none', zoomOnMouseWheel: true, moveOnMouseMove: true, moveOnMouseWheel: false, startValue: win.value.start, endValue: win.value.end },
      { type: 'slider', filterMode: 'none', height: 20, bottom: 8, startValue: win.value.start, endValue: win.value.end, borderColor: 'transparent', backgroundColor: dark ? '#1a2130' : '#f3f4f6', fillerColor: dark ? 'rgba(145,167,255,.18)' : 'rgba(59,91,219,.12)', dataBackground: { lineStyle: { color: dark ? '#4b5566' : '#cbd5e1' }, areaStyle: { color: dark ? '#2b3446' : '#e5e7eb' } }, textStyle: { color: dark ? '#9aa4b5' : '#6b7280' }, labelFormatter: (v: number) => fmtTime(v, { seconds: false, date: true }) }
    ],
    series: [
      { ...mk('上传', c.upload, r.up.points, showUp.value), markArea: { silent: true, itemStyle: { color: dark ? 'rgba(255,135,135,.10)' : 'rgba(224,49,49,.07)' }, data: gaps.map((g) => [{ xAxis: g.at }, { xAxis: g.end }]) }, markLine: { silent: true, symbol: 'none', lineStyle: { type: 'dashed', color: dark ? '#ffa94d' : '#e8590c', width: 1 }, label: { show: true, position: 'insideEndTop', fontSize: 10, formatter: (x: any) => x.name }, data: lines.map((l) => ({ xAxis: l.at, name: lineName(l.kind) })) } },
      mk('下载', c.download, r.down.points, showDown.value),
      ext('上传极值', c.upload, r.up.extremes, showExtremes.value && showUp.value),
      ext('下载极值', c.download, r.down.extremes, showExtremes.value && showDown.value)
    ]
  }
  ignoreZoom = true
  chart.setOption(opt, { notMerge: true })
  window.setTimeout(() => (ignoreZoom = false), 50)
}
function lineName(k: string) {
  return ({ counter_reset: '计数重置', composition_change: '组成变化', clock_change: '时钟异常', torrent_added: '首次观察', torrent_removed: '已移除', address_change: '地址变更' } as Record<string, string>)[k] || k
}
watch([metric, counter], () => load())
watch([showExtremes, showUp, showDown], draw)
watch(() => [theme.global.current.value.dark, prefs.utc], draw)
watch(win, (w) => {
  customStart.value = toLocalInput(w.start)
  customEnd.value = toLocalInput(w.end)
})
let ro: ResizeObserver | undefined
onMounted(() => {
  ro = new ResizeObserver(() => chart?.resize())
  if (el.value) ro.observe(el.value)
  customStart.value = toLocalInput(win.value.start)
  customEnd.value = toLocalInput(win.value.end)
  load()
})
onBeforeUnmount(() => {
  controller?.abort()
  if (refresh) window.clearTimeout(refresh)
  if (debounce) window.clearTimeout(debounce)
  ro?.disconnect()
  chart?.dispose()
})
defineExpose({ reload: load })
</script>
