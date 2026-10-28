<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { BarChart, CustomChart } from 'echarts/charts'
import { DataZoomComponent, GridComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { CustomSeriesRenderItemAPI, CustomSeriesRenderItemParams, CustomSeriesRenderItemReturn } from 'echarts'
import { datetime, duration, goDuration, parseDuration, statusText, typeText } from '@/format'
import type { Job, Schedule } from '@/types'

echarts.use([CustomChart, BarChart, GridComponent, TooltipComponent, DataZoomComponent, MarkLineComponent, MarkAreaComponent, CanvasRenderer])

// GanttChart draws the predicted serial execution of a host's projects (PRD
// S-10), the actual jobs below each prediction (S-12) and the planned work per
// hour with overloaded hours in red (S-11).
const props = defineProps<{ schedule: Schedule; projects: string[] }>()

const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

const typeColor = { full: '#1d4f91', incr: '#79a6e0' }
const statusColor: Record<string, string> = {
  success: '#67c23a',
  warning: '#e6a23c',
  failed: '#f56c6c',
  canceled: '#909399',
  skipped: '#c0c4cc',
  running: '#409eff',
  pending: '#c0c4cc',
}

const t = (s?: string) => (s ? new Date(s).getTime() : NaN)

function render() {
  if (!chart) return
  const s = props.schedule
  const rows = [...props.projects]
  for (const r of [...s.runs.map((r) => r.project), ...s.actual.map((j) => j.project)]) if (!rows.includes(r)) rows.push(r)
  const idx = (p: string) => rows.indexOf(p)
  const from = t(s.from)
  const to = t(s.to)

  // [row, start, end, kind, index]
  const predicted = s.runs
    .map((r, i) => (r.superseded ? [idx(r.project), t(r.at), t(r.at), 'superseded', i] : [idx(r.project), t(r.start), t(r.end), r.type, i]))
  const actual = s.actual
    .filter((j) => j.started_at)
    .map((j, i) => [idx(j.project), t(j.started_at), j.finished_at ? t(j.finished_at) : Date.now(), j.status, i])
  const planned = s.runs.map((r, i) => [idx(r.project), t(r.at), i])

  const renderBar =
    (lane: 'top' | 'bottom') =>
    (params: CustomSeriesRenderItemParams, api: CustomSeriesRenderItemAPI): CustomSeriesRenderItemReturn => {
      const row = api.value(0) as number
      const start = api.coord([api.value(1), row])
      const end = api.coord([api.value(2), row])
      const h = (api.size!([0, 1]) as number[])[1]
      const kind = api.value(3) as string
      const coordSys = params.coordSys as unknown as { x: number; y: number; width: number; height: number }
      if (kind === 'superseded') {
        return {
          type: 'text',
          style: { x: start[0], y: start[1] - h * 0.18, text: '×', fill: '#909399', font: 'bold 12px sans-serif', align: 'center', verticalAlign: 'middle' },
        }
      }
      const y = lane === 'top' ? start[1] - h * 0.38 : start[1] + h * 0.06
      const height = lane === 'top' ? h * 0.36 : h * 0.26
      const shape = echarts.graphic.clipRectByRect(
        { x: start[0], y, width: Math.max(end[0] - start[0], 2), height },
        { x: coordSys.x, y: coordSys.y, width: coordSys.width, height: coordSys.height },
      )
      if (!shape) return null as unknown as CustomSeriesRenderItemReturn
      const fill = lane === 'top' ? typeColor[kind as 'full' | 'incr'] : (statusColor[kind] ?? '#909399')
      return { type: 'rect', shape, style: { fill, stroke: lane === 'top' ? '#fff' : undefined, lineWidth: 0.5 } }
    }

  const hours = s.hours.map((h) => ({
    value: [t(h.hour) + 1800_000, parseDuration(h.total) / 60],
    itemStyle: { color: h.overloaded ? '#f56c6c' : '#91b5e8' },
    hour: h,
  }))

  chart.setOption(
    {
      animation: false,
      tooltip: {
        trigger: 'item',
        confine: true,
        formatter: (p: { seriesId: string; data: unknown }) => {
          const d = p.data as number[] | { hour: { hour: string; total: string; overloaded: boolean } }
          if (p.seriesId === 'load') {
            const h = (d as { hour: { hour: string; total: string; overloaded: boolean } }).hour
            return `${datetime(h.hour)} 起一小时<br/>计划工作量 ${goDuration(h.total)}${h.overloaded ? '<br/><b style="color:#f56c6c">超负荷（> 60 分钟）</b>' : ''}`
          }
          if (p.seriesId === 'actual') {
            const j = s.actual.filter((j) => j.started_at)[(d as number[])[4]] as Job
            return `<b>${j.project}</b> ${typeText[j.type]}（实际）<br/>状态 ${statusText[j.status]}<br/>开始 ${datetime(j.started_at)}<br/>结束 ${datetime(j.finished_at)}`
          }
          const r = s.runs[(d as number[])[p.seriesId === 'planned' ? 2 : 4]]
          if (r.superseded) return `<b>${r.project}</b> ${typeText[r.type]}<br/>计划 ${datetime(r.at)}<br/>被后一个同类计划覆盖（跳过）`
          const wait = (t(r.start) - t(r.at)) / 1000
          return `<b>${r.project}</b> ${typeText[r.type]}（预测）<br/>计划 ${datetime(r.at)}<br/>预计开始 ${datetime(r.start)}${wait > 0 ? `（排队 ${duration(wait)}）` : ''}<br/>预计结束 ${datetime(r.end)}<br/>预计耗时 ${goDuration(r.estimate)}`
        },
      },
      grid: [
        { left: 120, right: 30, top: 10, height: Math.max(rows.length * 42, 120) },
        { left: 120, right: 30, top: Math.max(rows.length * 42, 120) + 54, height: 120 },
      ],
      xAxis: [
        { type: 'time', min: from, max: to, gridIndex: 0, axisLabel: { hideOverlap: true }, splitLine: { show: true, lineStyle: { color: '#eee' } } },
        { type: 'time', min: from, max: to, gridIndex: 1, axisLabel: { hideOverlap: true } },
      ],
      yAxis: [
        { type: 'category', data: rows, inverse: true, gridIndex: 0, axisTick: { show: false }, splitLine: { show: true, lineStyle: { color: '#f2f2f2' } } },
        { type: 'value', gridIndex: 1, name: '分钟', max: (v: { max: number }) => Math.max(75, Math.ceil(v.max / 15) * 15) },
      ],
      dataZoom: [
        { type: 'slider', xAxisIndex: [0, 1], bottom: 4, height: 18 },
        { type: 'inside', xAxisIndex: [0, 1] },
      ],
      series: [
        { id: 'predicted', type: 'custom', renderItem: renderBar('top'), encode: { x: [1, 2], y: 0 }, data: predicted, xAxisIndex: 0, yAxisIndex: 0 },
        {
          id: 'actual',
          type: 'custom',
          renderItem: renderBar('bottom'),
          encode: { x: [1, 2], y: 0 },
          data: actual,
          xAxisIndex: 0,
          yAxisIndex: 0,
          markLine: {
            silent: true,
            symbol: 'none',
            lineStyle: { color: '#e6a23c', type: 'dashed' },
            label: { show: false },
            data: Date.now() > from && Date.now() < to ? [{ xAxis: Date.now() }] : [],
          },
        },
        {
          id: 'planned',
          type: 'custom',
          renderItem: (_p: CustomSeriesRenderItemParams, api: CustomSeriesRenderItemAPI) => {
            const c = api.coord([api.value(1), api.value(0)])
            const h = (api.size!([0, 1]) as number[])[1]
            return { type: 'line', shape: { x1: c[0], y1: c[1] - h * 0.42, x2: c[0], y2: c[1] - h * 0.02 }, style: { stroke: '#303133', lineWidth: 1 } }
          },
          encode: { x: 1, y: 0 },
          data: planned,
          xAxisIndex: 0,
          yAxisIndex: 0,
          z: 3,
        },
        {
          id: 'load',
          type: 'bar',
          xAxisIndex: 1,
          yAxisIndex: 1,
          barWidth: '60%',
          data: hours,
          markLine: {
            silent: true,
            symbol: 'none',
            lineStyle: { color: '#f56c6c' },
            label: { formatter: '60 分钟', position: 'insideEndTop' },
            data: [{ yAxis: 60 }],
          },
        },
      ],
    },
    true,
  )
  chart.resize({ height: Math.max(rows.length * 42, 120) + 240 })
}

let observer: ResizeObserver | null = null
onMounted(() => {
  chart = echarts.init(el.value!)
  render()
  observer = new ResizeObserver(() => chart?.resize({ height: Math.max(props.projects.length * 42, 120) + 240 }))
  observer.observe(el.value!)
})
onBeforeUnmount(() => {
  observer?.disconnect()
  chart?.dispose()
  chart = null
})
watch(() => [props.schedule, props.projects], render)
</script>

<template>
  <div class="legend">
    <span><i :style="{ background: typeColor.full }" />预测：全量</span>
    <span><i :style="{ background: typeColor.incr }" />预测：增量</span>
    <span><i class="tick" />计划时间</span>
    <span><b class="cross">×</b>被覆盖（跳过）</span>
    <span><i class="now" />当前时间</span>
    <span>实际执行：</span>
    <span v-for="k in ['success', 'warning', 'failed', 'running', 'canceled']" :key="k"><i :style="{ background: statusColor[k] }" />{{ statusText[k] }}</span>
    <span><i style="background: #f56c6c" />超负荷小时（计划工作量 &gt; 60 分钟）</span>
  </div>
  <div ref="el" class="gantt" />
</template>

<style scoped>
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  margin-bottom: 8px;
}
.legend i {
  display: inline-block;
  width: 12px;
  height: 10px;
  margin-right: 4px;
  vertical-align: -1px;
}
.legend i.tick {
  width: 1px;
  height: 12px;
  background: #303133;
}
.legend i.now {
  width: 0;
  height: 12px;
  border-left: 1px dashed #e6a23c;
}
.cross {
  color: #909399;
  margin-right: 4px;
}
.gantt {
  width: 100%;
  min-height: 360px;
}
</style>
