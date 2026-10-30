<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { api, errorMessage } from '@/api'
import { bytes, datetime, typeText } from '@/format'
import type { RestoreGuide } from '@/types'
import { ChatDotRound } from '@element-plus/icons-vue'
import { ask, enabled as assistantEnabled } from '@/composables/assistant'
import { locale, t } from '@/i18n'

// RestoreGuide shows the commands that restore a backup point (PRD J-6).
const props = defineProps<{ jobId: string | null }>()
const emit = defineEmits<{ close: [] }>()

const guide = ref<RestoreGuide | null>(null)
const error = ref('')
const loading = ref(false)

// The server writes the steps in the request's language, so a language
// switch fetches the guide again.
watch(
  () => [props.jobId, locale.value] as const,
  async ([id]) => {
    guide.value = null
    error.value = ''
    if (!id) return
    loading.value = true
    try {
      guide.value = await api.get<RestoreGuide>(`/jobs/${id}/restore`)
    } catch (e) {
      error.value = errorMessage(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

function explain() {
  if (!guide.value) return
  ask(t('restore.askExplain'), { job_id: guide.value.target.id })
  emit('close')
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(t('common.copied'))
  } catch {
    ElMessage.warning(t('common.copyFailed'))
  }
}
</script>

<template>
  <el-dialog :model-value="!!jobId" :title="$t('jobDrawer.restoreGuide')" width="860px" top="5vh" @close="emit('close')">
    <div v-loading="loading">
      <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" />
      <template v-if="guide">
        <i18n-t keypath="restore.intro" tag="p">
          <template #project><b>{{ guide.target.project }}</b></template>
          <template #time><b>{{ datetime(guide.target.finished_at) }}</b></template>
          <template #type>{{ typeText[guide.target.type] }}</template>
          <template #n>{{ guide.sequence.length }}</template>
        </i18n-t>
        <el-table :data="guide.sequence" size="small" class="seq">
          <el-table-column type="index" label="#" width="50" />
          <el-table-column :label="$t('job.type')" width="110"><template #default="{ row }">{{ typeText[row.type] }}</template></el-table-column>
          <el-table-column :label="$t('restore.finishedAt')" width="170"><template #default="{ row }">{{ datetime(row.finished_at) }}</template></el-table-column>
          <el-table-column :label="$t('job.size')" width="100"><template #default="{ row }">{{ bytes(row.compressed_size) }}</template></el-table-column>
          <el-table-column :label="$t('job.file')"><template #default="{ row }"><span class="mono">{{ row.path }}</span></template></el-table-column>
        </el-table>
        <el-descriptions :column="2" border size="small" class="seq">
          <el-descriptions-item :label="$t('job.host')">{{ guide.environment.host }}</el-descriptions-item>
          <el-descriptions-item :label="$t('projects.container')">{{ guide.environment.container || $t('common.unknown') }}</el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.image')"><span class="mono">{{ guide.environment.image || $t('common.unknown') }}</span></el-descriptions-item>
          <el-descriptions-item :label="$t('restore.dataDir')"><span class="mono">{{ guide.environment.data_dir || $t('hosts.notReported') }}</span></el-descriptions-item>
          <el-descriptions-item :label="$t('restore.dir')" :span="2"><span class="mono">{{ guide.environment.dir }}</span></el-descriptions-item>
        </el-descriptions>
        <el-alert v-if="guide.notes.length" type="warning" :closable="false" show-icon class="seq" :title="$t('restore.check')">
          <ul class="notes">
            <li v-for="n in guide.notes" :key="n">{{ n }}</li>
          </ul>
        </el-alert>
        <div v-for="(s, i) in guide.steps" :key="i" class="step">
          <div class="step-title">
            <b>{{ i + 1 }}. {{ s.title }}</b>
            <el-button size="small" link type="primary" @click="copy(s.commands)">{{ $t('common.copy') }}</el-button>
          </div>
          <pre class="code">{{ s.commands }}</pre>
        </div>
      </template>
    </div>
    <template v-if="assistantEnabled && guide" #footer>
      <el-button :icon="ChatDotRound" @click="explain">{{ $t('restore.explain') }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.seq {
  margin-bottom: 16px;
}
.notes {
  margin: 4px 0 0;
  padding-left: 18px;
  line-height: 1.7;
}
.step {
  margin-bottom: 14px;
}
.step-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}
</style>
