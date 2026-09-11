import { createApp } from 'vue'
import { createVuetify } from 'vuetify'
// Components and directives are registered explicitly: without this, unknown
// <v-*> tags silently render as empty custom elements (inputs disappear).
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'
import { aliases, mdi } from 'vuetify/iconsets/mdi-svg'
import 'vuetify/styles'
import { zhHans } from 'vuetify/locale'
import App from './App.vue'
import { router } from './router'
import './styles.css'

const vuetify = createVuetify({
  components,
  directives,
  icons: { defaultSet: 'mdi', aliases, sets: { mdi } },
  locale: { locale: 'zhHans', messages: { zhHans } },
  defaults: {
    VCard: { elevation: 0, rounded: 'lg' },
    VBtn: { rounded: 'lg' },
    VTextField: { variant: 'outlined', density: 'comfortable' },
    VSelect: { variant: 'outlined', density: 'comfortable' },
    VChip: { size: 'small' }
  },
  theme: {
    defaultTheme: 'light',
    themes: {
      light: {
        colors: {
          primary: '#3b5bdb',
          secondary: '#0ca678',
          surface: '#ffffff',
          background: '#f4f6fb',
          error: '#e03131',
          warning: '#e8590c',
          success: '#2f9e44',
          info: '#1c7ed6',
          upload: '#3b5bdb',
          download: '#0ca678'
        }
      },
      dark: {
        dark: true,
        colors: {
          primary: '#91a7ff',
          secondary: '#63e6be',
          surface: '#161b27',
          background: '#0f1218',
          error: '#ff8787',
          warning: '#ffa94d',
          success: '#8ce99a',
          info: '#74c0fc',
          upload: '#91a7ff',
          download: '#63e6be'
        }
      }
    }
  }
})

createApp(App).use(vuetify).use(router).mount('#app')
