<script setup lang="ts">
import { errorCodeText, statusTag, statusText } from '@/format'

defineProps<{ status: string; code?: string; message?: string }>()
</script>

<template>
  <el-tooltip :disabled="!code && !message" :content="[code && (errorCodeText[code] ?? code), message].filter(Boolean).join('：')" placement="top">
    <el-tag :type="statusTag[status]" size="small" disable-transitions>
      {{ statusText[status] ?? status }}<template v-if="code"> · {{ errorCodeText[code] ?? code }}</template>
    </el-tag>
  </el-tooltip>
</template>
