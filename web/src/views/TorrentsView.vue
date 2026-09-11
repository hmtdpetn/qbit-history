<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>任务列表</h1>
        <p>分类和标签仅用于展示与筛选，不会写回 qB。1h / 24h 有效上传来自累计计数差，覆盖不足时如实显示。</p>
      </div>
      <div class="text-caption" style="opacity: .7">{{ total }} 个任务 · 当前速度每 3 秒刷新，历史统计每 60 秒更新</div>
    </div>
    <v-card border class="mb-3">
      <div class="d-flex flex-wrap ga-2 pa-3">
        <v-text-field v-model="search" label="搜索名称 / hash" density="compact" hide-details clearable :prepend-inner-icon="mdiMagnify" style="min-width: 220px; flex: 2" @update:model-value="debouncedLoad" />
        <v-select v-model="category" :items="facet(categories, '全部分类')" label="分类" density="compact" hide-details style="min-width: 150px; flex: 1" @update:model-value="load" />
        <v-select v-model="tag" :items="facet(tags, '全部标签')" label="标签" density="compact" hide-details style="min-width: 150px; flex: 1" @update:model-value="load" />
        <v-select v-model="stateFilter" :items="stateItems" label="状态" density="compact" hide-details style="min-width: 150px; flex: 1" @update:model-value="load" />
        <v-select v-model="sort" :items="sortItems" label="排序" density="compact" hide-details style="min-width: 160px; flex: 1" @update:model-value="load" />
        <v-btn variant="outlined" density="comfortable" :icon="order === 'desc' ? mdiSortDescending : mdiSortAscending" :aria-label="order === 'desc' ? '降序' : '升序'" @click="order = order === 'desc' ? 'asc' : 'desc'; load()" />
      </div>
    </v-card>
    <v-card border>
      <v-data-table-virtual
        :headers="headers"
        :items="items"
        :height="tableHeight"
        item-value="torrent_id"
        density="comfortable"
        fixed-header
        hover
        class="torrent-table"
        :mobile="xs"
        @click:row="(_: any, r: any) => open(r.item)"
      >
        <template #item.name="{ item }">
          <div class="d-flex align-center ga-2" style="min-width: 0">
            <span class="dot" :class="item.deletion_candidate ? 'warn' : item.current?.up && Number(item.current.up) > 0 ? 'good' : 'idle'" :title="item.deletion_candidate ? '正在确认移除' : ''"></span>
            <div style="min-width: 0">
              <div class="truncate torrent-name" :title="item.name" style="max-width: 46vw">{{ item.name || '（名称未知）' }}</div>
              <div class="text-caption truncate" style="opacity: .65">{{ instanceName(item.instance_id) }}<template v-if="item.category"> · {{ item.category }}</template><template v-if="item.tags"> · {{ item.tags }}</template><template v-if="item.deletion_candidate"> · <span class="text-warning">正在确认移除</span></template></div>
            </div>
          </div>
        </template>
        <template #item.state="{ item }"><v-chip size="x-small" variant="tonal" :color="stateColor(item.state)">{{ stateText(item.state) }}</v-chip></template>
        <template #item.speed="{ item }"><span class="mono num">↑{{ fmtSpeed(item.current?.up) }}<br />↓{{ fmtSpeed(item.current?.down) }}</span></template>
        <template #item.uploaded="{ item }"><span class="mono num">{{ fmtBytes(item.current?.uploaded) }}</span></template>
        <template #item.ratio="{ item }"><span class="num">{{ item.ratio?.toFixed(2) ?? '—' }}</span></template>
        <template #item.upload_1h="{ item }">
          <div class="mono num">{{ item.upload_1h === null ? '统计中' : fmtBytes(item.upload_1h) }}</div>
          <div v-if="lowCoverage(item)" class="text-caption text-warning" style="line-height: 1.2">覆盖 {{ Math.round(item.stats_coverage * 100) }}%</div>
        </template>
        <template #item.upload_24h="{ item }">
          <div class="mono num">{{ item.upload_24h === null ? '统计中' : fmtBytes(item.upload_24h) }}</div>
          <div v-if="lowCoverage(item)" class="text-caption text-warning" style="line-height: 1.2">覆盖 {{ Math.round(item.stats_coverage * 100) }}%</div>
        </template>
        <template #item.last_upload_at="{ item }"><span class="text-caption">{{ item.last_upload_at ? fmtAgo(item.last_upload_at, tick) : '未观察到' }}</span></template>
        <template #no-data><div class="py-8 text-medium-emphasis">没有匹配的任务</div></template>
      </v-data-table-virtual>
      <div v-if="nextCursor" class="pa-3 text-center"><v-btn variant="text" @click="more">加载更多（已显示 {{ items.length }} / {{ total }}）</v-btn></div>
    </v-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useDisplay } from 'vuetify'
import { mdiMagnify, mdiSortAscending, mdiSortDescending } from '@mdi/js'
import { api } from '../api'
import { state, instanceName, type Torrent } from '../store'
import { fmtBytes, fmtSpeed, fmtAgo, stateText, stateColor } from '../format'

const router = useRouter()
const { xs, height } = useDisplay()
const items = ref<Torrent[]>([])
const total = ref(0)
const nextCursor = ref('')
const categories = ref<Record<string, number>>({})
const tags = ref<Record<string, number>>({})
const states = ref<Record<string, number>>({})
const search = ref('')
const category = ref('')
const tag = ref('')
const stateFilter = ref('')
const sort = ref('name')
const order = ref<'asc' | 'desc'>('asc')
const tick = ref(Date.now())
const tableHeight = computed(() => Math.max(360, height.value - 300))
const headers: any[] = [
  { title: '任务', key: 'name', sortable: false, minWidth: '260px' },
  { title: '状态', key: 'state', sortable: false, width: '120px' },
  { title: '上传 / 下载', key: 'speed', sortable: false, width: '140px' },
  { title: '累计上传', key: 'uploaded', sortable: false, width: '110px' },
  { title: '分享率', key: 'ratio', sortable: false, width: '80px' },
  { title: '1h 有效上传', key: 'upload_1h', sortable: false, width: '120px' },
  { title: '24h 有效上传', key: 'upload_24h', sortable: false, width: '120px' },
  { title: '最近上传', key: 'last_upload_at', sortable: false, width: '110px' }
]
const sortItems = [
  { title: '名称', value: 'name' }, { title: '当前上传', value: 'up' }, { title: '当前下载', value: 'down' }, { title: '累计上传', value: 'uploaded' }, { title: '分享率', value: 'ratio' }, { title: '大小', value: 'size' },
  { title: '1h 有效上传', value: 'upload_1h' }, { title: '24h 有效上传', value: 'upload_24h' }, { title: '最近上传', value: 'last_upload' }, { title: '添加时间', value: 'added' }, { title: '状态', value: 'state' }
]
const stateItems = computed(() => [{ title: '全部状态', value: '' }, ...Object.keys(states.value).sort().map((s) => ({ title: `${stateText(s)} (${states.value[s]})`, value: s }))])
function facet(m: Record<string, number>, all: string) {
  return [{ title: all, value: '' }, ...Object.keys(m).sort().map((k) => ({ title: `${k || '（无）'} (${m[k]})`, value: k }))]
}
// Coverage is shown separately when the 24 h window is only partly observed,
// so a small number is never mistaken for "this torrent barely uploaded".
function lowCoverage(t: Torrent) {
  return t.upload_24h !== null && t.stats_coverage < 0.9
}
function query(cursor = '') {
  const p = new URLSearchParams({ limit: '300', sort: sort.value, order: order.value })
  if (search.value) p.set('search', search.value)
  if (category.value) p.set('category', category.value)
  if (tag.value) p.set('tag', tag.value)
  if (stateFilter.value) p.set('state', stateFilter.value)
  if (state.selectedInstance !== 'all') p.set('instance', state.selectedInstance)
  if (cursor) p.set('cursor', cursor)
  return '/torrents?' + p.toString()
}
async function load() {
  try {
    const r = await api(query())
    items.value = r.items || []
    total.value = r.total
    nextCursor.value = r.next_cursor || ''
    categories.value = r.categories || {}
    tags.value = r.tags || {}
    states.value = r.states || {}
  } catch (e: any) {
    state.error = e.message
  }
}
async function more() {
  const r = await api(query(nextCursor.value))
  items.value = items.value.concat(r.items || [])
  nextCursor.value = r.next_cursor || ''
}
let deb: number | undefined
function debouncedLoad() {
  if (deb) window.clearTimeout(deb)
  deb = window.setTimeout(load, 250)
}
function open(t: Torrent) {
  router.push(`/torrents/${t.torrent_id}`)
}
let timer: number
onMounted(() => {
  load()
  timer = window.setInterval(() => {
    tick.value = Date.now()
    if (!nextCursor.value || items.value.length <= 300) load()
  }, 3000)
})
onUnmounted(() => window.clearInterval(timer))
watch(() => state.selectedInstance, load)
</script>

<style>
.torrent-table td { vertical-align: middle; }
</style>
