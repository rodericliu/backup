<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { api, errorMessage } from '@/api'
import { bytes, datetime, typeText } from '@/format'
import type { RestoreGuide } from '@/types'

// RestoreGuide shows the commands that restore a backup point (PRD J-6).
const props = defineProps<{ jobId: string | null }>()
const emit = defineEmits<{ close: [] }>()

const guide = ref<RestoreGuide | null>(null)
const error = ref('')
const loading = ref(false)

watch(
  () => props.jobId,
  async (id) => {
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

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('浏览器不允许复制，请手动选择')
  }
}
</script>

<template>
  <el-dialog :model-value="!!jobId" title="恢复指引" width="860px" top="5vh" @close="emit('close')">
    <div v-loading="loading">
      <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" />
      <template v-if="guide">
        <p>
          恢复 <b>{{ guide.target.project }}</b> 到 <b>{{ datetime(guide.target.finished_at) }}</b>（{{ typeText[guide.target.type] }}备份完成时的状态），按顺序使用
          {{ guide.sequence.length }} 份备份：
        </p>
        <el-table :data="guide.sequence" size="small" class="seq">
          <el-table-column type="index" label="#" width="50" />
          <el-table-column label="类型" width="70"><template #default="{ row }">{{ typeText[row.type] }}</template></el-table-column>
          <el-table-column label="完成时间" width="170"><template #default="{ row }">{{ datetime(row.finished_at) }}</template></el-table-column>
          <el-table-column label="大小" width="100"><template #default="{ row }">{{ bytes(row.compressed_size) }}</template></el-table-column>
          <el-table-column label="文件"><template #default="{ row }"><span class="mono">{{ row.path }}</span></template></el-table-column>
        </el-table>
        <el-alert
          title="在一台单独的恢复机器或临时目录中操作；/restore 与数据目录路径按实际情况调整。prepare 需要与备份相同的 xtrabackup 2.4 版本。"
          type="info"
          show-icon
          :closable="false"
          class="seq"
        />
        <div v-for="(s, i) in guide.steps" :key="i" class="step">
          <div class="step-title">
            <b>{{ i + 1 }}. {{ s.title }}</b>
            <el-button size="small" link type="primary" @click="copy(s.commands)">复制</el-button>
          </div>
          <pre class="code">{{ s.commands }}</pre>
        </div>
      </template>
    </div>
  </el-dialog>
</template>

<style scoped>
.seq {
  margin-bottom: 16px;
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
