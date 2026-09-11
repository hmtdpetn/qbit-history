<template>
  <v-app :theme="activeTheme">
    <LoginView v-if="session.checked && !session.authenticated" />
    <template v-else-if="session.authenticated">
      <v-app-bar flat border density="comfortable">
        <v-app-bar-nav-icon v-if="mobile" :icon="mdiMenu" aria-label="打开菜单" @click="drawer = !drawer" />
        <div class="d-flex align-center ga-2 px-3" style="font-weight: 800; letter-spacing: -0.02em">
          <span class="brand-mark">qh</span>
          <span v-if="!xs">qbit-history</span>
        </div>
        <v-spacer />
        <v-select
          v-model="state.selectedInstance"
          :items="instanceItems"
          item-title="title"
          item-value="value"
          density="compact"
          hide-details
          variant="outlined"
          :style="{ maxWidth: xs ? '160px' : '260px' }"
          aria-label="选择实例"
          class="mr-2"
        >
          <template #item="{ props: p, item }">
            <v-list-item v-bind="p">
              <template #prepend><span class="dot mr-3" :class="item.raw.dotClass"></span></template>
            </v-list-item>
          </template>
        </v-select>
        <v-menu>
          <template #activator="{ props: p }">
            <v-btn v-bind="p" :icon="themeIcon" variant="text" aria-label="切换主题" />
          </template>
          <v-list density="compact">
            <v-list-item v-for="t in themeOptions" :key="t.value" :title="t.title" :active="prefs.theme === t.value" @click="setTheme(t.value)" />
          </v-list>
        </v-menu>
        <v-menu>
          <template #activator="{ props: p }">
            <v-btn v-bind="p" :icon="mdiAccountCircleOutline" variant="text" aria-label="账户" />
          </template>
          <v-list density="compact">
            <v-list-item :title="session.user" :subtitle="`v${session.version} · 时区 ${prefs.utc ? 'UTC' : localZone}`" />
            <v-list-item :title="prefs.utc ? '改用本地时区显示' : '改用 UTC 显示'" @click="setUTC(!prefs.utc)" />
            <v-list-item title="修改密码" :to="{ name: 'settings', hash: '#password' }" />
            <v-divider />
            <v-list-item title="退出登录" @click="doLogout" />
          </v-list>
        </v-menu>
      </v-app-bar>
      <v-navigation-drawer v-model="drawer" :temporary="mobile" :permanent="!mobile" width="232" border>
        <v-list nav density="comfortable" class="pt-3">
          <v-list-item v-for="n in nav" :key="n.to" :to="n.to" :prepend-icon="n.icon" :title="n.title" rounded="lg" exact />
        </v-list>
        <template #append>
          <div class="pa-4 text-caption" style="opacity: .65; line-height: 1.7">
            只读上游 · 独立认证<br />采集不依赖浏览器在线<br />
            <span v-if="state.loadedAt">状态更新 {{ fmtAgo(state.loadedAt, tick) }}</span>
          </div>
        </template>
      </v-navigation-drawer>
      <v-main>
        <v-alert v-if="state.error" type="error" variant="tonal" density="compact" class="ma-3" closable @click:close="state.error = ''">{{ state.error }}</v-alert>
        <v-alert v-if="state.managerError" type="warning" variant="tonal" density="compact" class="ma-3">{{ state.managerError }}</v-alert>
        <v-alert v-if="state.storage.paused" type="warning" variant="tonal" density="compact" class="ma-3" :icon="mdiDatabaseAlertOutline">
          历史写入已暂停：{{ state.storage.reason }}。当前状态仍在更新；回收空间后会自动恢复。
        </v-alert>
        <v-alert v-for="i in bulkProtected" :key="i.instance.instance_id" type="warning" variant="tonal" density="compact" class="ma-3">
          实例「{{ i.instance.name }}」有 {{ i.deletion_candidates }} 个任务同时消失，已启用批量删除保护；历史暂不清理。请到“连接管理”确认。
        </v-alert>
        <router-view />
      </v-main>
      <v-snackbar v-model="snack" :timeout="4000" location="bottom">{{ state.notice }}</v-snackbar>
    </template>
    <div v-else class="login-bg"><v-progress-circular indeterminate /></div>
  </v-app>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useDisplay } from 'vuetify'
import { mdiMenu, mdiViewDashboardOutline, mdiFormatListBulleted, mdiLanConnect, mdiDatabaseCogOutline, mdiHeartPulse, mdiWhiteBalanceSunny, mdiWeatherNight, mdiThemeLightDark, mdiAccountCircleOutline, mdiDatabaseAlertOutline } from '@mdi/js'
import { session, checkSession, logout } from './api'
import { state, startPolling, stopPolling } from './store'
import { prefs, setTheme, setUTC, localZone, fmtAgo, connColor, connText } from './format'
import LoginView from './views/LoginView.vue'

const { mobile, xs } = useDisplay()
const drawer = ref(!mobile.value)
const tick = ref(Date.now())
const snack = ref(false)
watch(() => state.notice, (v) => (snack.value = !!v))
watch(snack, (v) => { if (!v) state.notice = '' })

const nav = [
  { to: '/', icon: mdiViewDashboardOutline, title: '总览' },
  { to: '/torrents', icon: mdiFormatListBulleted, title: '任务列表' },
  { to: '/connections', icon: mdiLanConnect, title: '连接管理' },
  { to: '/settings', icon: mdiDatabaseCogOutline, title: '存储与设置' },
  { to: '/status', icon: mdiHeartPulse, title: '服务状态' }
]
const themeOptions = [
  { value: 'system' as const, title: '跟随系统' },
  { value: 'light' as const, title: '浅色' },
  { value: 'dark' as const, title: '深色' }
]
const systemDark = ref(window.matchMedia('(prefers-color-scheme: dark)').matches)
const mq = window.matchMedia('(prefers-color-scheme: dark)')
const onMq = (e: MediaQueryListEvent) => (systemDark.value = e.matches)
const activeTheme = computed(() => (prefs.theme === 'system' ? (systemDark.value ? 'dark' : 'light') : prefs.theme))
const themeIcon = computed(() => (prefs.theme === 'system' ? mdiThemeLightDark : prefs.theme === 'dark' ? mdiWeatherNight : mdiWhiteBalanceSunny))
const instanceItems = computed(() => [
  { title: `全部实例 (${state.instances.length})`, value: 'all', dotClass: 'idle' },
  ...state.instances.map((i) => ({ title: `${i.instance.name} · ${connText(i.connection_status)}`, value: i.instance.instance_id, dotClass: connColor(i.connection_status) }))
])
const bulkProtected = computed(() => state.instances.filter((i) => i.bulk_delete_protection))

async function doLogout() {
  await logout()
}
let t: number
onMounted(async () => {
  mq.addEventListener('change', onMq)
  t = window.setInterval(() => (tick.value = Date.now()), 1000)
  if (await checkSession()) startPolling()
})
onUnmounted(() => {
  mq.removeEventListener('change', onMq)
  window.clearInterval(t)
  stopPolling()
})
</script>
