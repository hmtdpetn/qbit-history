<template>
  <div class="login-bg">
    <v-card width="min(420px, 100%)" class="pa-8" border>
      <div class="d-flex align-center ga-3 mb-2">
        <span class="brand-mark" style="width: 44px; height: 44px; font-size: 16px; border-radius: 12px">qh</span>
        <div>
          <div style="font-size: 22px; font-weight: 800; letter-spacing: -0.02em">qbit-history</div>
          <div class="text-caption" style="opacity: .65">qBittorrent 历史监控 · 本地登录</div>
        </div>
      </div>
      <form class="mt-6" @submit.prevent="submit">
        <v-text-field v-model="name" label="管理员账号" autocomplete="username" :prepend-inner-icon="mdiAccountOutline" required />
        <v-text-field v-model="password" label="密码" type="password" autocomplete="current-password" :prepend-inner-icon="mdiLockOutline" required />
        <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mb-4">{{ error }}</v-alert>
        <v-btn type="submit" color="primary" block size="large" :loading="busy">登录</v-btn>
      </form>
      <p class="text-caption mt-6 mb-0" style="opacity: .6; line-height: 1.6">
        此账号只用于本应用。已保存的 qB 密码不会回传到浏览器。登录失败 10 次后将限制 15 分钟。
      </p>
    </v-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { mdiAccountOutline, mdiLockOutline } from '@mdi/js'
import { login } from '../api'
import { startPolling } from '../store'

const name = ref('admin')
const password = ref('')
const error = ref('')
const busy = ref(false)
async function submit() {
  busy.value = true
  error.value = ''
  try {
    await login(name.value, password.value)
    password.value = ''
    startPolling()
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>
