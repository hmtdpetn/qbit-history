import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue'), meta: { title: '总览' } },
    { path: '/torrents', name: 'torrents', component: () => import('./views/TorrentsView.vue'), meta: { title: '任务列表' } },
    { path: '/torrents/:id', name: 'torrent', component: () => import('./views/TorrentDetailView.vue'), meta: { title: '任务详情' } },
    { path: '/instances/:id/torrent-by-key/:key', name: 'torrent-by-key', component: () => import('./views/TorrentDetailView.vue'), meta: { title: '任务详情' } },
    { path: '/connections', name: 'connections', component: () => import('./views/ConnectionsView.vue'), meta: { title: '连接管理' } },
    { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue'), meta: { title: '存储与设置' } },
    { path: '/status', name: 'status', component: () => import('./views/StatusView.vue'), meta: { title: '服务状态' } },
    { path: '/:pathMatch(.*)*', redirect: '/' }
  ]
})
router.afterEach((to) => {
  document.title = `${to.meta.title || 'qbit-history'} · qbit-history`
})
