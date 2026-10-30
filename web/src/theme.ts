import { computed, ref, watch } from 'vue'

// Theme preference (light, dark or follow the system), applied as the
// `dark` class on <html> that Element Plus's dark CSS variables key off.

export type ThemePreference = 'light' | 'dark' | 'system'

const storageKey = 'backup-theme'
const media = window.matchMedia('(prefers-color-scheme: dark)')

function stored(): ThemePreference {
  const v = localStorage.getItem(storageKey)
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'system'
}

export const themePreference = ref<ThemePreference>(stored())
const systemDark = ref(media.matches)
media.addEventListener('change', (e) => (systemDark.value = e.matches))

export const isDark = computed(() => (themePreference.value === 'system' ? systemDark.value : themePreference.value === 'dark'))

watch(
  isDark,
  (dark) => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  },
  { immediate: true },
)

export function setTheme(t: ThemePreference) {
  themePreference.value = t
  localStorage.setItem(storageKey, t)
}

// Chart colors that follow the theme, mirroring the CSS variables in style.css.
export const chartColors = computed(() =>
  isDark.value
    ? {
        full: '#8490F7',
        incr: '#4F578F',
        success: '#3BC98A',
        warning: '#F0B429',
        failed: '#FF7B7F',
        muted: '#7A8494',
        faint: '#3A4150',
        running: '#74C4F8',
        load: '#3A4065',
        grid: '#2C323F',
        gridFaint: '#222733',
        text: '#9AA4B6',
        now: '#DDE3ED',
        surface: '#1A1E27',
      }
    : {
        full: '#4E58C4',
        incr: '#A7ACE2',
        success: '#16754A',
        warning: '#975705',
        failed: '#BE2E38',
        muted: '#838CA0',
        faint: '#C9D0DD',
        running: '#1266B5',
        load: '#CACDED',
        grid: '#DDE3EC',
        gridFaint: '#EDF0F6',
        text: '#5C6472',
        now: '#1F2530',
        surface: '#FFFFFF',
      },
)
