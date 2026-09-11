<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>连接管理</h1>
        <p>手动注册 qB WebUI 地址。应用只调用登录、版本、sync/maindata 与 torrents/info 只读接口，不会改变 qB 的任务或配置。</p>
      </div>
      <v-btn color="primary" :prepend-icon="mdiPlus" @click="startNew">新增连接</v-btn>
    </div>
    <div class="grid-2">
      <v-card border class="card-pad">
        <div class="card-title"><h2>{{ editId ? '编辑连接' : '添加连接' }}</h2><span v-if="editId" class="sub">ID {{ editId.slice(0, 8) }}…</span></div>
        <v-form @submit.prevent="save">
          <v-text-field v-model="form.name" label="显示名称" placeholder="例如 qB-下载机" :rules="[required]" />
          <v-text-field v-model="form.base_url" label="qB WebUI 地址（从 history 容器出发）" placeholder="例如 http://qbittorrent:8080" :rules="[required]" hint="容器内 127.0.0.1 指向 history 自己；优先填写唯一容器名 / 可解析 DNS 名 + 内部端口。反代前缀可写在路径中。" persistent-hint />
          <v-text-field v-model="form.username" label="qB 用户名" autocomplete="off" :rules="[required]" class="mt-2" />
          <v-text-field v-model="form.password" :label="editId ? '密码（留空 = 不修改已保存的密码）' : 'qB 密码'" type="password" autocomplete="new-password" :rules="editId ? [] : [required]" />
          <v-switch v-model="form.poll_enabled" label="启用采集" color="primary" density="compact" hide-details class="mb-3" />
          <v-alert v-if="message" :type="messageType" variant="tonal" density="compact" class="mb-3">{{ message }}</v-alert>
          <div class="d-flex flex-wrap ga-2">
            <v-btn variant="outlined" :loading="testing" :disabled="!form.base_url || !form.username || (!form.password && !editId)" @click="test">测试连接</v-btn>
            <v-btn type="submit" color="primary" :loading="saving">{{ editId ? '保存修改' : '测试并保存' }}</v-btn>
            <v-btn v-if="editId" variant="text" @click="startNew">取消</v-btn>
          </div>
        </v-form>
        <div class="help-box mt-4">
          <b>地址与 Docker 网络</b><br />
          两个 qB 容器的内部端口可以都是 8080；只有映射到宿主同一 IP 时宿主端口才需要区分。history 容器需要加入 qB 所在的外部网络才能用容器名解析。若两个网络里都有同名别名（如 <code>qbittorrent</code>），请使用核查得到的唯一容器名。
          修改地址应指向同一台 qB；换成另一台客户端请新建连接，否则两台的历史会被拼接。
        </div>
      </v-card>
      <v-card border class="card-pad">
        <div class="card-title"><h2>已注册连接</h2><span class="sub">{{ state.instances.length }} 个</span></div>
        <div v-if="!state.instances.length" class="text-medium-emphasis py-8 text-center">当前没有已注册连接。</div>
        <div v-for="i in state.instances" :key="i.instance.instance_id" class="py-3" style="border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity))">
          <div class="d-flex align-start ga-3">
            <span class="dot mt-2" :class="connColor(i.connection_status)"></span>
            <div style="min-width: 0; flex: 1">
              <div class="d-flex align-center ga-2 flex-wrap"><b>{{ i.instance.name }}</b><v-chip size="x-small" variant="tonal">{{ connText(i.connection_status) }}</v-chip><v-chip v-if="!i.instance.credentials_saved" size="x-small" color="error" variant="tonal">缺少凭据</v-chip></div>
              <div class="text-caption mono truncate" style="opacity: .75">{{ i.instance.base_url }}</div>
              <div class="text-caption" style="opacity: .65">用户 {{ i.instance.username }} · 已保存凭据（不回显） · {{ i.qb_version || '版本未知' }} · {{ i.torrent_count }} 个任务</div>
              <div v-if="i.error" class="text-caption text-error">{{ i.error }}</div>
              <v-alert v-if="i.bulk_delete_protection" type="warning" variant="tonal" density="compact" class="mt-2">
                {{ i.deletion_candidates }} 个任务同时从该 qB 消失（批量删除保护）。只有你确认后才会清除这些任务在本应用中的历史；不会触碰 qB。
                <div class="mt-2"><v-btn size="small" color="warning" variant="flat" @click="confirmBulk(i)">只清除此实例已消失任务的历史</v-btn></div>
              </v-alert>
              <div v-else-if="i.deletion_candidates" class="text-caption text-warning">{{ i.deletion_candidates }} 个任务正在确认移除</div>
            </div>
          </div>
          <div class="d-flex flex-wrap ga-1 mt-2 pl-5">
            <v-btn size="small" variant="text" :prepend-icon="mdiPencilOutline" @click="edit(i)">编辑</v-btn>
            <v-btn size="small" variant="text" :prepend-icon="i.instance.poll_enabled ? mdiPauseCircleOutline : mdiPlayCircleOutline" @click="toggle(i)">{{ i.instance.poll_enabled ? '停止采集' : '恢复采集' }}</v-btn>
            <v-btn size="small" variant="text" :prepend-icon="mdiRefresh" @click="reconnect(i)">重连</v-btn>
            <v-btn size="small" variant="text" color="error" :prepend-icon="mdiDeleteOutline" @click="askRemove(i)">移除</v-btn>
          </div>
        </div>
      </v-card>
    </div>
    <v-dialog v-model="removeOpen" max-width="480">
      <v-card class="pa-5">
        <h2 class="mb-2" style="font-size: 18px">移除连接「{{ removing?.instance.name }}」？</h2>
        <p class="text-body-2">这会停止采集并<b>清除该实例在本应用中的全部历史</b>（分批删除，几分钟内完成）。<br />不会删除 qB 中的任务、文件或配置。若只是暂时不想采集，请使用“停止采集”。</p>
        <v-text-field v-model="removeConfirm" label="输入连接名称以确认" density="compact" class="mt-2" />
        <div class="d-flex justify-end ga-2"><v-btn variant="text" @click="removeOpen = false">取消</v-btn><v-btn color="error" :disabled="removeConfirm !== removing?.instance.name" :loading="removingBusy" @click="remove">清除历史并移除</v-btn></div>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { mdiPlus, mdiPencilOutline, mdiPauseCircleOutline, mdiPlayCircleOutline, mdiRefresh, mdiDeleteOutline } from '@mdi/js'
import { api } from '../api'
import { state, refreshStatus, type Instance } from '../store'
import { connColor, connText } from '../format'

const required = (v: string) => (!!v && v.trim() !== '') || '必填'
const blank = () => ({ name: '', base_url: '', username: '', password: '', poll_enabled: true })
const form = ref(blank())
const editId = ref('')
const message = ref('')
const messageType = ref<'success' | 'error' | 'info'>('info')
const testing = ref(false)
const saving = ref(false)
const removeOpen = ref(false)
const removing = ref<Instance | null>(null)
const removeConfirm = ref('')
const removingBusy = ref(false)

function startNew() {
  editId.value = ''
  form.value = blank()
  message.value = ''
}
function edit(i: Instance) {
  editId.value = i.instance.instance_id
  form.value = { name: i.instance.name, base_url: i.instance.base_url, username: i.instance.username, password: '', poll_enabled: i.instance.poll_enabled }
  message.value = ''
  window.scrollTo({ top: 0, behavior: 'smooth' })
}
function show(type: 'success' | 'error' | 'info', m: string) {
  messageType.value = type
  message.value = m
}
async function test() {
  testing.value = true
  try {
    if (editId.value && !form.value.password) {
      await api(`/instances/${editId.value}/reconnect`, { method: 'POST' })
      show('info', '已用已保存的凭据重新连接；结果稍后显示在右侧状态中')
    } else {
      const r = await api('/instances/test', { method: 'POST', body: JSON.stringify({ base_url: form.value.base_url, username: form.value.username, password: form.value.password }) })
      show('success', `${r.message}：qB ${r.qb_version}，WebAPI ${r.webapi_version}，地址规范化为 ${r.normalized_url}`)
    }
  } catch (e: any) {
    show('error', e.message)
  } finally {
    testing.value = false
  }
}
async function save() {
  saving.value = true
  try {
    const body: any = { name: form.value.name, base_url: form.value.base_url, username: form.value.username, poll_enabled: form.value.poll_enabled }
    if (form.value.password) body.password = form.value.password
    if (editId.value) {
      const r = await api(`/instances/${editId.value}`, { method: 'PATCH', body: JSON.stringify(body) })
      show('success', r.address_changed ? '已保存；地址已变更，历史按同一实例延续。' : '已保存；密码不会回显。')
    } else {
      body.password = form.value.password
      await api('/instances', { method: 'POST', body: JSON.stringify(body) })
      show('success', '已测试并保存；采集已开始。')
      form.value = blank()
    }
    form.value.password = ''
    await refreshStatus()
  } catch (e: any) {
    show('error', e.message)
  } finally {
    saving.value = false
  }
}
async function toggle(i: Instance) {
  try {
    await api(`/instances/${i.instance.instance_id}`, { method: 'PATCH', body: JSON.stringify({ poll_enabled: !i.instance.poll_enabled }) })
    state.notice = i.instance.poll_enabled ? '已停止采集，历史保留' : '已恢复采集'
    await refreshStatus()
  } catch (e: any) {
    show('error', e.message)
  }
}
async function reconnect(i: Instance) {
  await api(`/instances/${i.instance.instance_id}/reconnect`, { method: 'POST' }).catch((e) => show('error', e.message))
  state.notice = '已重新连接所有采集器'
}
async function confirmBulk(i: Instance) {
  try {
    const r = await api(`/instances/${i.instance.instance_id}/confirm-bulk-removal`, { method: 'POST' })
    state.notice = r.message
    await refreshStatus()
  } catch (e: any) {
    show('error', e.message)
  }
}
function askRemove(i: Instance) {
  removing.value = i
  removeConfirm.value = ''
  removeOpen.value = true
}
async function remove() {
  if (!removing.value) return
  removingBusy.value = true
  const id = removing.value.instance.instance_id
  try {
    await api(`/instances/${id}/confirm-history-purge`, { method: 'POST' })
    await api(`/instances/${id}`, { method: 'DELETE' })
    removeOpen.value = false
    state.notice = '连接已移除；历史正在分批清理'
    if (editId.value === id) startNew()
    await refreshStatus()
  } catch (e: any) {
    show('error', e.message)
  } finally {
    removingBusy.value = false
  }
}
</script>
