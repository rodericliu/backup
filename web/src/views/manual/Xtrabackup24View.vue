<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import PageHeader from '@/components/PageHeader.vue'
import { locale, t } from '@/i18n'
import zhSource from './xtrabackup24.md?raw'
import enSource from './xtrabackup24.en.md?raw'

interface TocItem {
  id: string
  text: string
  depth: number
}

const escape = (s: string) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)

// render converts the manual to sanitized HTML with numbered heading ids and
// collects the table of contents.
function render(source: string, copyLabel: string) {
  const toc: TocItem[] = []
  const md = new Marked({
    gfm: true,
    renderer: {
      heading({ tokens, depth, text }) {
        const id = `sec-${toc.length + 1}`
        if (depth <= 3) toc.push({ id, text, depth })
        return `<h${depth} id="${id}">${this.parser.parseInline(tokens)}</h${depth}>`
      },
      code({ text, lang }) {
        const label = lang ? `<span class="code-lang">${escape(lang)}</span>` : ''
        return `<div class="code-block">${label}<button type="button" class="code-copy">${escape(copyLabel)}</button><pre><code>${escape(text)}</code></pre></div>`
      },
    },
  })
  return { html: DOMPurify.sanitize(md.parse(source, { async: false }) as string), toc }
}

const doc = computed(() => render(locale.value === 'en' ? enSource : zhSource, t('common.copy')))

const article = ref<HTMLElement>()
const current = ref('')

function go(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  current.value = id
}

async function onClick(e: MouseEvent) {
  const el = e.target as HTMLElement
  const copy = el.closest('.code-copy')
  if (copy) {
    const code = copy.parentElement?.querySelector('pre')?.textContent ?? ''
    try {
      await navigator.clipboard.writeText(code)
      ElMessage.success(t('common.copied'))
    } catch {
      ElMessage.warning(t('common.copyFailed'))
    }
    return
  }
  const href = el.closest('a')?.getAttribute('href')
  if (href && /^https?:\/\//.test(href)) {
    e.preventDefault()
    window.open(href, '_blank', 'noopener,noreferrer')
  }
}

// Highlight the last heading scrolled past the top of the viewport.
let observer: IntersectionObserver | undefined
function observe() {
  observer?.disconnect()
  current.value = doc.value.toc[0]?.id ?? ''
  const headings = article.value?.querySelectorAll<HTMLElement>('h2[id], h3[id]')
  if (!headings?.length) return
  const visible = new Set<string>()
  observer = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.isIntersecting) visible.add(e.target.id)
        else visible.delete(e.target.id)
      }
      const first = doc.value.toc.find((item) => visible.has(item.id))
      if (first) current.value = first.id
    },
    { rootMargin: '-80px 0px -60% 0px' },
  )
  headings.forEach((h) => observer!.observe(h))
}
onMounted(observe)
watch(doc, () => nextTick(observe))
onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <div class="bk-page">
    <PageHeader :title="$t('nav.xtrabackup')" :eyebrow="$t('nav.manual')" :description="$t('manual.description')" />
    <div class="manual">
      <!-- eslint-disable-next-line vue/no-v-html -- static content, sanitized by DOMPurify -->
      <article ref="article" class="bk-panel markdown" @click="onClick" v-html="doc.html" />
      <nav class="toc" :aria-label="$t('manual.toc')">
        <div class="toc-title">{{ $t('manual.toc') }}</div>
        <a
          v-for="item in doc.toc"
          :key="item.id"
          :href="`#${item.id}`"
          class="toc-link"
          :class="[`depth-${item.depth}`, { active: current === item.id }]"
          @click.prevent="go(item.id)"
        >{{ item.text }}</a>
      </nav>
    </div>
  </div>
</template>

<style scoped>
.manual {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 220px;
  align-items: start;
  gap: 24px;
}

.toc {
  position: sticky;
  inset-block-start: calc(var(--bk-navbar-height) + 24px);
  max-block-size: calc(100dvh - var(--bk-navbar-height) - 48px);
  overflow-y: auto;
  padding-inline-start: 12px;
  border-inline-start: 1px solid var(--bk-border);
}
.toc-title {
  margin-block-end: 8px;
  color: var(--bk-secondary);
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.08em;
}
.toc-link {
  display: block;
  padding: 4px 0;
  color: var(--bk-secondary);
  font-size: 0.8125rem;
  line-height: 1.5;
  text-decoration: none;
}
.toc-link:hover {
  color: var(--bk-on-surface);
  text-decoration: none;
}
.toc-link.depth-3 {
  padding-inline-start: 12px;
  font-size: 0.75rem;
}
.toc-link.active {
  color: var(--el-color-primary);
  font-weight: 600;
}

@media (max-width: 1099px) {
  .manual {
    grid-template-columns: minmax(0, 1fr);
  }
  .toc {
    display: none;
  }
}

.markdown {
  padding: 28px var(--bk-panel-padding, 24px) 32px;
  color: var(--bk-on-surface);
  font-size: 14px;
  line-height: 1.8;
  word-break: break-word;
}
.markdown :deep(> :first-child) {
  margin-top: 0;
}
.markdown :deep(> :last-child) {
  margin-bottom: 0;
}
.markdown :deep(p),
.markdown :deep(ul),
.markdown :deep(ol),
.markdown :deep(table),
.markdown :deep(.code-block),
.markdown :deep(blockquote) {
  margin: 0 0 14px;
}
.markdown :deep(h2),
.markdown :deep(h3),
.markdown :deep(h4) {
  scroll-margin-top: calc(var(--bk-navbar-height) + 16px);
  font-weight: 600;
  line-height: 1.4;
}
.markdown :deep(h2) {
  margin: 36px 0 14px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--bk-border);
  font-size: 20px;
}
.markdown :deep(h3) {
  margin: 24px 0 10px;
  font-size: 16px;
}
.markdown :deep(h4) {
  margin: 18px 0 8px;
  font-size: 14px;
}
.markdown :deep(ul),
.markdown :deep(ol) {
  padding-left: 22px;
}
.markdown :deep(li + li) {
  margin-top: 4px;
}
.markdown :deep(code) {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--bk-subtle);
  color: var(--bk-code);
  font-family: var(--bk-font-data);
  font-size: 12.5px;
}
.markdown :deep(.code-block) {
  position: relative;
}
.markdown :deep(.code-block pre) {
  margin: 0;
  padding: 14px 16px;
  overflow: auto;
  border: 1px solid var(--bk-border);
  border-radius: 8px;
  background: var(--bk-subtle);
  font-family: var(--bk-font-data);
  font-size: 12.5px;
  line-height: 1.6;
}
.markdown :deep(.code-block pre code) {
  padding: 0;
  background: none;
  color: var(--bk-on-surface);
}
.markdown :deep(.code-copy) {
  position: absolute;
  top: 6px;
  right: 6px;
  padding: 2px 8px;
  border: 1px solid var(--bk-border);
  border-radius: 6px;
  background: var(--bk-surface);
  color: var(--bk-secondary);
  font-size: 11px;
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--bk-motion);
}
.markdown :deep(.code-block:hover .code-copy),
.markdown :deep(.code-copy:focus-visible) {
  opacity: 1;
}
.markdown :deep(.code-lang) {
  position: absolute;
  top: -9px;
  left: 10px;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--bk-surface);
  color: var(--bk-secondary);
  font-size: 10px;
}
.markdown :deep(table) {
  display: block;
  max-width: 100%;
  overflow-x: auto;
  border-collapse: collapse;
  font-size: 13px;
}
.markdown :deep(th),
.markdown :deep(td) {
  padding: 7px 12px;
  border: 1px solid var(--bk-border);
  text-align: left;
  vertical-align: top;
}
.markdown :deep(th) {
  background: var(--bk-table-header);
  font-weight: 600;
  white-space: nowrap;
}
.markdown :deep(td code) {
  white-space: nowrap;
}
.markdown :deep(blockquote) {
  padding: 8px 14px;
  border-left: 3px solid var(--el-color-primary);
  border-radius: 0 6px 6px 0;
  background: rgba(var(--bk-primary-rgb), 0.06);
  color: var(--bk-on-surface);
}
.markdown :deep(blockquote p) {
  margin: 0;
}
.markdown :deep(a) {
  color: var(--el-color-primary);
}
</style>
