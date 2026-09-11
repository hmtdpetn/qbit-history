<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>存储与设置</h1>
        <p>采样间隔、保留天数和数据预算对所有连接生效。最近 24 小时始终保留逐秒核心数据；改成 30 天不会立刻出现过去 30 天的数据。</p>
      </div>
    </div>
    <div class="grid-2">
      <v-card border class="card-pad">
        <div class="card-title"><h2>采集与保留</h2></div>
        <v-select v-model="form.interval_seconds" :items="[{ title: '1 秒（默认）', value: 1 }, { title: '2 秒', value: 2 }, { title: '5 秒', value: 5 }]" label="全局目标采样间隔" hint="对所有实例、所有任务统一生效；不会按活跃度区别对待" persistent-hint class="mb-3" />
        <v-select v-model="form.retention_days" :items="[{ title: '7 天（默认）', value: 7 }, { title: '14 天', value: 14 }, { title: '30 天', value: 30 }]" label="滚动保留" hint="延长只从现在起累积；缩短会不可逆地删除超出范围的历史" persistent-hint class="mb-3" />
        <v-select v-model="form.data_budget_bytes" :items="[{ title: '2 GiB（默认）', value: 2147483648 }, { title: '1 GiB', value: 1073741824 }]" label="数据预算" hint="应用级预算与紧急保护，不是 Docker 硬配额；接近预算时先告警、清理到期数据，超过安全线暂停历史写入" persistent-hint class="mb-3" />
        <v-expansion-panels variant="accordion" class="mb-3">
          <v-expansion-panel title="高级">
            <v-expansion-panel-text>
              <v-text-field v-model.number="form.timeout_ms" type="number" label="上游 HTTP 总超时（毫秒）" hint="500–10000；同实例请求不重叠" persistent-hint class="mb-3" />
              <v-text-field v-model.number="form.response_limit_bytes" type="number" label="响应大小上限（字节）" hint="1 MiB–64 MiB；超限视为失败，不截断处理" persistent-hint />
            </v-expansion-panel-text>
          </v-expansion-panel>
        </v-expansion-panels>
        <div class="help-box mb-3">
          <b>精度规则</b>：0–24 小时逐秒无损；24–72 小时 60 秒摘要（保留桶内最高/最低及其时间、累计端点）；72 小时–{{ form.retention_days }} 天 5 分钟摘要；更早删除。旧数据 ±5% 以内的平稳区间在图上显示为趋势直线，不改变累计字节计数。
        </div>
        <v-alert v-if="msg" :type="msgType" variant="tonal" density="compact" class="mb-3">{{ msg }}</v-alert>
        <v-btn color="primary" :loading="saving" @click="save()">保存设置</v-btn>
      </v-card>
      <div>
        <v-card border class="card-pad mb-4">
          <div class="card-title"><h2>容量预测</h2><span class="sub">分层模型 × 实测字节率，不是用最近净增长外推</span></div>
          <div class="d-flex flex-column ga-2">
            <div v-for="f in forecast" :key="f.days" class="pa-3 rounded-lg" :style="{ background: 'rgba(var(--v-theme-on-surface), .045)', outline: f.days === form.retention_days ? '2px solid rgb(var(--v-theme-primary))' : 'none' }">
              <div class="d-flex align-center justify-space-between"><b>{{ f.days }} 天</b><v-chip size="x-small" :color="f.over_budget ? 'error' : 'success'" variant="tonal">{{ f.over_budget ? '可能超过当前预算' : '在预算范围内' }}</v-chip></div>
              <div class="num" style="font-size: 20px; font-weight: 700">{{ fmtBytes(f.bytes_low) }} – {{ fmtBytes(f.bytes_high) }}</div>
              <div class="text-caption" style="opacity: .7">{{ f.series }} 条序列 · raw {{ f.raw_bytes_per_sample.toFixed(1) }} B/样本 · 60s {{ f.minute_bytes_per_bucket.toFixed(0) }} B/桶 · 300s {{ f.five_bytes_per_bucket.toFixed(0) }} B/桶</div>
              <div class="text-caption" :class="f.calibrated ? 'text-success' : 'text-warning'">{{ f.confidence }}</div>
            </div>
          </div>
        </v-card>
        <v-card border class="card-pad mb-4">
          <div class="card-title"><h2>当前空间明细</h2><span class="sub">统计于 {{ fmtAgo(s.stats_at) }}</span></div>
          <div class="kv">
            <div><div class="k">主库文件</div><div class="v num">{{ fmtBytes(s.main_bytes) }}</div></div>
            <div><div class="k">WAL / SHM</div><div class="v num">{{ fmtBytes(s.wal_bytes) }} / {{ fmtBytes(s.shm_bytes) }}</div></div>
            <div><div class="k">数据目录合计</div><div class="v num">{{ fmtBytes(s.directory_bytes) }} / {{ fmtBytes(s.budget_bytes) }}</div></div>
            <div><div class="k">已用页 / 可复用页</div><div class="v num">{{ fmtBytes((s.used_pages || 0) * (s.page_size || 0)) }} / {{ fmtBytes((s.free_pages || 0) * (s.page_size || 0)) }}</div></div>
            <div><div class="k">原始样本载荷</div><div class="v num">{{ fmtBytes(s.payload_raw_bytes) }} · {{ (s.raw_samples || 0).toLocaleString() }} 样本</div></div>
            <div><div class="k">60 秒摘要</div><div class="v num">{{ fmtBytes(s.payload_60_bytes) }} · {{ (s.buckets_60 || 0).toLocaleString() }} 桶</div></div>
            <div><div class="k">300 秒摘要</div><div class="v num">{{ fmtBytes(s.payload_300_bytes) }} · {{ (s.buckets_300 || 0).toLocaleString() }} 桶</div></div>
            <div><div class="k">共享（索引/元数据/页内空隙）</div><div class="v num">{{ fmtBytes(s.shared_bytes) }}</div></div>
            <div><div class="k">宿主可用空间</div><div class="v num">{{ s.host_free_bytes >= 0 ? fmtBytes(s.host_free_bytes) : '未知' }}</div></div>
            <div><div class="k">待写队列</div><div class="v num">{{ fmtBytes(s.queue_bytes) }} · 已丢弃 {{ s.queue_dropped || 0 }}</div></div>
          </div>
          <v-progress-linear :model-value="Math.min(100, ((s.directory_bytes || 0) / (s.budget_bytes || 1)) * 100)" :color="s.paused ? 'error' : 'primary'" height="8" rounded class="mt-3" />
          <div class="text-caption mt-1" style="opacity: .7">按实例/任务的有效载荷归属见“服务状态”；SQLite 物理字节无法精确平摊。</div>
        </v-card>
        <v-card id="password" border class="card-pad">
          <div class="card-title"><h2>修改管理员密码</h2></div>
          <v-text-field v-model="pw.current" label="当前密码" type="password" autocomplete="current-password" />
          <v-text-field v-model="pw.next" label="新密码（12–72 个字符）" type="password" autocomplete="new-password" />
          <v-alert v-if="pwMsg" type="info" variant="tonal" density="compact" class="mb-3">{{ pwMsg }}</v-alert>
          <v-btn variant="outlined" :disabled="pw.next.length < 12 || !pw.current" @click="changePw">更新密码并重新登录</v-btn>
        </v-card>
      </div>
    </div>
    <v-dialog v-model="confirmOpen" max-width="440">
      <v-card class="pa-5">
        <h2 style="font-size: 18px" class="mb-2">缩短保留期</h2>
        <p class="text-body-2">从 {{ state.settings.retention_days }} 天改为 {{ form.retention_days }} 天会在接下来的维护周期中<b>不可逆地删除</b>超出范围的历史。确定继续？</p>
        <div class="d-flex justify-end ga-2"><v-btn variant="text" @click="confirmOpen = false">取消</v-btn><v-btn color="error" @click="save(true)">确认缩短</v-btn></div>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, session } from '../api'
import { state, refreshStatus, type Settings } from '../store'
import { fmtBytes, fmtAgo } from '../format'

const form = ref<Settings>({ ...state.settings })
const forecast = ref<any[]>([])
const saving = ref(false)
const msg = ref('')
const msgType = ref<'success' | 'error'>('success')
const confirmOpen = ref(false)
const pw = ref({ current: '', next: '' })
const pwMsg = ref('')
const s = computed(() => state.storage)

async function loadForecast() {
  try {
    const r = await api('/storage/forecast')
    forecast.value = r.forecast || []
  } catch {}
}
async function save(confirmed = false) {
  if (form.value.retention_days < state.settings.retention_days && !confirmed) {
    confirmOpen.value = true
    return
  }
  confirmOpen.value = false
  saving.value = true
  try {
    await api('/settings', { method: 'PATCH', body: JSON.stringify(form.value) }, { headers: confirmed ? { 'X-Confirm-Retention': 'yes' } : {} })
    msgType.value = 'success'
    msg.value = form.value.retention_days > state.settings.retention_days ? '已保存；延长的保留期从现在开始累积，已删除的历史不会恢复。' : '设置已保存。'
    await refreshStatus()
    await loadForecast()
  } catch (e: any) {
    msgType.value = 'error'
    msg.value = e.message
  } finally {
    saving.value = false
  }
}
async function changePw() {
  try {
    const r = await api('/auth/password', { method: 'POST', body: JSON.stringify({ current_password: pw.value.current, new_password: pw.value.next }) })
    pwMsg.value = r.message
    session.authenticated = false
  } catch (e: any) {
    pwMsg.value = e.message
  }
}
watch(() => state.settings, (v) => { if (!saving.value) form.value = { ...v } }, { deep: true })
onMounted(loadForecast)
</script>
