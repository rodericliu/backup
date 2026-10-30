<script setup lang="ts">
defineProps<{ title: string; description?: string; eyebrow?: string }>()
</script>

<template>
  <header class="page-header">
    <div class="page-header__copy">
      <p class="page-header__eyebrow">{{ eyebrow || $t('app.name') }}</p>
      <div class="page-header__title-row">
        <h1>{{ title }}</h1>
        <slot name="badge" />
      </div>
      <p v-if="description" class="page-header__description">{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="page-header__actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.page-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
}
.page-header__copy {
  min-inline-size: 0;
  flex: 1 1 280px;
}
.page-header__eyebrow {
  margin: 0 0 8px;
  color: var(--bk-secondary);
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.page-header__title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}
h1 {
  margin: 0;
  color: var(--bk-on-surface);
  font-size: clamp(1.5rem, 2.4vw, 2rem);
  font-weight: 650;
  letter-spacing: -0.035em;
  line-height: 1.25;
  overflow-wrap: anywhere;
}
.page-header__description {
  margin: 8px 0 0;
  color: var(--bk-secondary);
  font-size: 0.875rem;
  line-height: 1.6;
  max-inline-size: 72ch;
}
.page-header__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.page-header__actions :deep(.el-button + .el-button) {
  margin-left: 0;
}
@media (max-width: 599px) {
  .page-header__actions {
    inline-size: 100%;
  }
}
</style>
