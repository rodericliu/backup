import { computed, watch } from 'vue'
import { createI18n } from 'vue-i18n'
import zhCN from './zh-CN'
import en from './en'

// UI language: Simplified Chinese or English. The first visit follows the
// browser's preferred language; an explicit choice is kept in localStorage.

export type Locale = 'zh-CN' | 'en'

export const locales: { value: Locale; label: string; short: string }[] = [
  { value: 'zh-CN', label: '简体中文', short: '中' },
  { value: 'en', label: 'English', short: 'EN' },
]

const storageKey = 'backup-locale'

function initial(): Locale {
  const v = localStorage.getItem(storageKey)
  if (v === 'zh-CN' || v === 'en') return v
  const preferred = navigator.languages?.[0] ?? navigator.language ?? ''
  return preferred.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: initial(),
  fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zhCN, en },
  // Messages are only rendered as text, so angle brackets such as
  // "<backup_root>" are harmless.
  warnHtmlMessage: false,
})

export const t = i18n.global.t

export const locale = computed(() => i18n.global.locale.value as Locale)

export function setLocale(l: Locale) {
  i18n.global.locale.value = l
  localStorage.setItem(storageKey, l)
}

watch(locale, (l) => (document.documentElement.lang = l), { immediate: true })

// keyed returns a read-only record whose values are the messages below
// prefix in the current language; unknown keys read as undefined. Reads in a
// render function track the language, so the text follows a switch.
export function keyed(prefix: string): Record<string, string> {
  return new Proxy({} as Record<string, string>, {
    get: (_, k) => {
      if (typeof k !== 'string') return undefined
      const key = `${prefix}.${k}`
      return i18n.global.te(key) || i18n.global.te(key, 'zh-CN') ? t(key) : undefined
    },
  })
}
