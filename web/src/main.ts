import { computed, createApp } from 'vue'
import ElementPlus, { provideGlobalConfig } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import enLocale from 'element-plus/es/locale/lang/en'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import App from './App.vue'
import router from './router'
import { i18n, locale } from './i18n'
import './theme'
import './style.css'

const app = createApp(App).use(i18n).use(router).use(ElementPlus)
// Element Plus follows the UI language, including message boxes created
// outside the component tree.
provideGlobalConfig(computed(() => ({ locale: locale.value === 'en' ? enLocale : zhCn })), app, true)
app.mount('#app')
