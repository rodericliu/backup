<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, errorMessage } from '@/api'
import { loadInfo } from '@/composables/assistant'
import { datetime } from '@/format'
import type { AssistantSettings, KeyTestResult } from '@/types'
import { t } from '@/i18n'

// The assistant API key, stored encrypted with the master key. The key is
// never sent back to the browser.
const settings = ref<AssistantSettings | null>(null)
const loading = ref(false)
const key = ref('')
const saving = ref(false)
const testing = ref(false)
const result = ref<KeyTestResult | null>(null)

async function load() {
  loading.value = true
  try {
    settings.value = await api.get<AssistantSettings>('/settings/assistant')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function applied(s: AssistantSettings) {
  settings.value = s
  key.value = ''
  result.value = null
  await loadInfo(true)
}

async function save() {
  if (!key.value.trim()) return
  saving.value = true
  try {
    await applied(await api.put<AssistantSettings>('/settings/assistant', { api_key: key.value }))
    ElMessage.success(t('aiKey.saved'))
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    saving.value = false
  }
}

// test checks the entered key, or the current one when none is entered.
async function test() {
  testing.value = true
  result.value = null
  try {
    result.value = await api.post<KeyTestResult>('/settings/assistant/test', { api_key: key.value.trim() || undefined })
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    testing.value = false
  }
}

async function remove() {
  await ElMessageBox.confirm(t('aiKey.removeConfirm'), t('aiKey.removeTitle'), { type: 'warning', confirmButtonText: t('common.delete') })
  try {
    await applied(await api.del<AssistantSettings>('/settings/assistant'))
    ElMessage.success(t('aiKey.removed'))
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
</script>

<template>
  <div v-loading="loading" class="bk-panel">
    <div v-if="settings" class="bk-panel__body">
      <el-alert v-if="!settings.enabled" type="info" :closable="false" show-icon :title="$t('aiKey.disabled')" class="alert" />
      <el-alert v-else-if="!settings.active" type="warning" :closable="false" show-icon :title="$t('aiKey.noKey')" class="alert" />
      <el-alert v-if="settings.error" type="error" :closable="false" show-icon :title="$t('aiKey.decryptError')" class="alert" />

      <el-descriptions :column="1" border class="info">
        <el-descriptions-item :label="$t('job.status')">
          <el-tag :type="settings.active ? 'success' : settings.enabled ? 'warning' : 'info'" size="small">
            {{ settings.active ? $t('aiKey.active') : settings.enabled ? $t('aiKey.inactive') : $t('aiKey.off') }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item :label="$t('aiKey.current')">
          <template v-if="settings.source">
            <code>{{ settings.hint }}</code>
            <span class="muted source">{{ $t(`aiKey.sources.${settings.source}`) }}</span>
          </template>
          <span v-else class="muted">{{ $t('aiKey.none') }}</span>
        </el-descriptions-item>
        <el-descriptions-item v-if="settings.source === 'database'" :label="$t('aiKey.updated')">
          {{ datetime(settings.updated_at) }} · {{ settings.updated_by }}
        </el-descriptions-item>
        <el-descriptions-item label="base_url"><code>{{ settings.base_url }}</code></el-descriptions-item>
        <el-descriptions-item :label="$t('aiKey.models')">{{ settings.models.join(', ') || '-' }}</el-descriptions-item>
      </el-descriptions>

      <h4 class="section-label">{{ settings.source === 'database' ? $t('aiKey.replace') : $t('aiKey.set') }}</h4>
      <div class="form">
        <el-input v-model="key" type="password" show-password autocomplete="off" placeholder="sk-..." style="width: 420px" @keyup.enter="save" />
        <el-button type="primary" :loading="saving" :disabled="!key.trim()" @click="save">{{ $t('common.save') }}</el-button>
        <el-button :loading="testing" :disabled="!key.trim() && !settings.active" @click="test">{{ $t('aiKey.test') }}</el-button>
        <el-button v-if="settings.source === 'database'" type="danger" plain @click="remove">{{ $t('aiKey.remove') }}</el-button>
      </div>
      <el-alert
        v-if="result"
        :type="result.ok ? 'success' : 'error'"
        :closable="false"
        show-icon
        :title="result.ok ? $t('aiKey.testOk', { model: result.model, ms: result.latency_ms }) : $t('aiKey.testFailed', { model: result.model })"
        :description="result.error"
        class="alert result"
      />
      <p class="muted hint">{{ $t('aiKey.hint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.alert {
  margin-bottom: 14px;
}
.info {
  max-width: 640px;
  margin-bottom: 20px;
}
.info :deep(.el-descriptions__label) {
  width: 160px;
}
.source {
  margin-left: 8px;
}
.form {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.result {
  margin-top: 14px;
  max-width: 640px;
}
.hint {
  margin-top: 14px;
  max-width: 640px;
  font-size: 12px;
}
</style>
