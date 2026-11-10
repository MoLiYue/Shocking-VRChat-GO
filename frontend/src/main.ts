import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './assets/global.css'
import './theme'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/dashboard', component: () => import('./views/Dashboard.vue') },
    { path: '/mode/shock', component: () => import('./views/ModeShock.vue') },
    { path: '/mode/distance', component: () => import('./views/ModeDistance.vue') },
    { path: '/mode/touch', component: () => import('./views/ModeTouch.vue') },
    { path: '/mode/combo', component: () => import('./views/ComboEditor.vue') },
    // Strength limits, parameter mapping and overlimit rules: one page.
    { path: '/params', component: () => import('./views/Setup.vue') },
    { path: '/sps', component: () => import('./views/Sps.vue') },
    { path: '/recorder', component: () => import('./views/Recorder.vue') },
    { path: '/monitor', component: () => import('./views/Monitor.vue') },
    { path: '/settings', component: () => import('./views/Settings.vue') },
    { path: '/strength', redirect: { path: '/params', hash: '#strength' } },
    { path: '/overlimit-rules', redirect: { path: '/params', hash: '#overlimit' } },
    { path: '/wave-test', component: () => import('./views/WaveTest.vue') },
    { path: '/logs', component: () => import('./views/Logs.vue') },
    { path: '/chatbox', component: () => import('./views/Chatbox.vue') },
    { path: '/modules', component: () => import('./views/Modules.vue') },
    // Legacy redirects
    { path: '/curve', redirect: '/mode/distance' },
    { path: '/combo', redirect: '/mode/combo' },
    { path: '/mode/curve', redirect: '/mode/distance' },
  ],
})

const app = createApp(App)
app.use(router)
app.mount('#app')
