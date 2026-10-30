<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { BarChart, CustomChart } from 'echarts/charts'
import { DataZoomComponent, GridComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { CustomSeriesRenderItemAPI, CustomSeriesRenderItemParams, CustomSeriesRenderItemReturn } from 'echarts'
import { datetime, duration, goDuration, parseDuration, statusText, typeText } from '@/format'
import type { Job, Schedule } from '@/types'
import { chartColors } from '@/theme'
import { locale, t } from '@/i18n'

echarts.use([CustomChart, BarChart, GridComponent, TooltipComponent, DataZoomComponent, MarkLineComponent, MarkAreaComponent, CanvasRenderer])

// GanttChart draws the predicted serial execution of a host's projects (PRD
// S-10), the actual jobs below each prediction (S-12) and the planned work per
// hour with overloaded hours in red (S-11).
const props = defineProps<{ schedule: Schedule; projects: string[] }>()

const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

const c = chartColors
const typeColor = computed(() => ({ full: c.value.full, incr: c.value.incr }))
const statusColor = computed<Record<string, string>>(() => ({
  success: c.value.success,
  warning: c.value.warning,
  failed: c.value.failed,
  canceled: c.value.muted,
  skipped: c.value.faint,
  running: c.value.running,
  pending: c.value.faint,
}))

const ms = (s?: string) => (s ? new Date(s).getTime() : NaN)

function render() {
  if (!chart) return
  const col = c.value
  const axis = { axisLabel: { color: col.text, hideOverlap: true }, axisLine: { lineStyle: { color: col.grid } }, axisTick: { lineStyle: { color: col.grid } } }
  const s = props.schedule
  const rows = [...props.projects]
  for (const r of [...s.runs.map((r) => r.project), ...s.actual.map((j) => j.project)]) if (!rows.includes(r)) rows.push(r)
  const idx = (p: string) => rows.indexOf(p)
  const from = ms(s.from)
  const to = ms(s.to)

  // [row, start, end, kind, index]
  const predicted = s.runs
    .map((r, i) => (r.superseded ? [idx(r.project), ms(r.at), ms(r.at), 'superseded', i] : [idx(r.project), ms(r.start), ms(r.end), r.type, i]))
  const actual = s.actual
    .filter((j) => j.started_at)
    .map((j, i) => [idx(j.project), ms(j.started_at), j.finished_at ? ms(j.finished_at) : Date.now(), j.status, i])
  const planned = s.runs.map((r, i) => [idx(r.project), ms(r.at), i])

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
          style: { x: start[0], y: start[1] - h * 0.18, text: '×', fill: col.muted, font: 'bold 12px sans-serif', align: 'center', verticalAlign: 'middle' },
        }
      }
      const y = lane === 'top' ? start[1] - h * 0.38 : start[1] + h * 0.06
      const height = lane === 'top' ? h * 0.36 : h * 0.26
      const shape = echarts.graphic.clipRectByRect(
        { x: start[0], y, width: Math.max(end[0] - start[0], 2), height },
        { x: coordSys.x, y: coordSys.y, width: coordSys.width, height: coordSys.height },
      )
      if (!shape) return null as unknown as CustomSeriesRenderItemReturn
      const fill = lane === 'top' ? typeColor.value[kind as 'full' | 'incr'] : (statusColor.value[kind] ?? col.muted)
      return { type: 'rect', shape: { ...shape, r: 2 }, style: { fill, stroke: lane === 'top' ? col.surface : undefined, lineWidth: 0.5 } }
    }

  const hours = s.hours.map((h) => ({
    value: [ms(h.hour) + 1800_000, parseDuration(h.total) / 60],
    itemStyle: { color: h.overloaded ? col.failed : col.load, borderRadius: [2, 2, 0, 0] },
    hour: h,
  }))

  chart.setOption(
    {
      animation: false,
      tooltip: {
        trigger: 'item',
        confine: true,
        backgroundColor: col.surface,
        borderColor: col.grid,
        textStyle: { color: col.now, fontSize: 12 },
        formatter: (p: { seriesId: string; data: unknown }) => {
          const d = p.data as number[] | { hour: { hour: string; total: string; overloaded: boolean } }
          if (p.seriesId === 'load') {
            const h = (d as { hour: { hour: string; total: string; overloaded: boolean } }).hour
            return `${t('gantt.hourFrom', { time: datetime(h.hour) })}<br/>${t('gantt.plannedWork', { d: goDuration(h.total) })}${h.overloaded ? `<br/><b style="color:${col.failed}">${t('gantt.overloaded')}</b>` : ''}`
          }
          if (p.seriesId === 'actual') {
            const j = s.actual.filter((j) => j.started_at)[(d as number[])[4]] as Job
            return `<b>${j.project}</b> ${t('gantt.actual', { type: typeText[j.type] })}<br/>${t('gantt.status', { s: statusText[j.status] })}<br/>${t('gantt.started', { time: datetime(j.started_at) })}<br/>${t('gantt.finished', { time: datetime(j.finished_at) })}`
          }
          const r = s.runs[(d as number[])[p.seriesId === 'planned' ? 2 : 4]]
          if (r.superseded) return `<b>${r.project}</b> ${typeText[r.type]}<br/>${t('gantt.planned', { time: datetime(r.at) })}<br/>${t('gantt.superseded')}`
          const wait = (ms(r.start) - ms(r.at)) / 1000
          return `<b>${r.project}</b> ${t('gantt.predicted', { type: typeText[r.type] })}<br/>${t('gantt.planned', { time: datetime(r.at) })}<br/>${t('gantt.expectedStart', { time: datetime(r.start) })}${wait > 0 ? t('gantt.queued', { d: duration(wait) }) : ''}<br/>${t('gantt.expectedEnd', { time: datetime(r.end) })}<br/>${t('gantt.expectedDuration', { d: goDuration(r.estimate) })}`
        },
      },
      grid: [
        { left: 120, right: 30, top: 10, height: Math.max(rows.length * 42, 120) },
        { left: 120, right: 30, top: Math.max(rows.length * 42, 120) + 54, height: 120 },
      ],
      xAxis: [
        { type: 'time', min: from, max: to, gridIndex: 0, ...axis, splitLine: { show: true, lineStyle: { color: col.grid } } },
        { type: 'time', min: from, max: to, gridIndex: 1, ...axis },
      ],
      yAxis: [
        { type: 'category', data: rows, inverse: true, gridIndex: 0, ...axis, axisLabel: { color: col.now }, axisTick: { show: false }, splitLine: { show: true, lineStyle: { color: col.gridFaint } } },
        { type: 'value', gridIndex: 1, name: t('gantt.minutes'), nameTextStyle: { color: col.text }, ...axis, splitLine: { lineStyle: { color: col.gridFaint } }, max: (v: { max: number }) => Math.max(75, Math.ceil(v.max / 15) * 15) },
      ],
      dataZoom: [
        { type: 'slider', xAxisIndex: [0, 1], bottom: 4, height: 18, borderColor: col.grid, fillerColor: 'rgba(132, 144, 247, 0.15)', textStyle: { color: col.text }, dataBackground: { lineStyle: { color: col.grid }, areaStyle: { color: col.gridFaint } } },
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
            lineStyle: { color: col.warning, type: 'dashed' },
            label: { show: false },
            data: Date.now() > from && Date.now() < to ? [{ xAxis: Date.now() }] : [],
          },
        },
        {
          id: 'planned',
          type: 'custom',
          renderItem: (_p: CustomSeriesRenderItemParams, api: CustomSeriesRenderItemAPI) => {
            const p = api.coord([api.value(1), api.value(0)])
            const h = (api.size!([0, 1]) as number[])[1]
            return { type: 'line', shape: { x1: p[0], y1: p[1] - h * 0.42, x2: p[0], y2: p[1] - h * 0.02 }, style: { stroke: col.now, lineWidth: 1 } }
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
            lineStyle: { color: col.failed },
            label: { formatter: t('gantt.sixtyMinutes'), position: 'insideEndTop', color: col.failed },
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
watch(() => [props.schedule, props.projects, c.value, locale.value], render)
</script>

<template>
  <div class="legend">
    <span><i :style="{ background: typeColor.full }" />{{ $t('gantt.legendFull') }}</span>
    <span><i :style="{ background: typeColor.incr }" />{{ $t('gantt.legendIncr') }}</span>
    <span><i class="tick" />{{ $t('gantt.legendPlanned') }}</span>
    <span><b class="cross">×</b>{{ $t('gantt.legendSuperseded') }}</span>
    <span><i class="now" />{{ $t('gantt.legendNow') }}</span>
    <span>{{ $t('gantt.legendActual') }}</span>
    <span v-for="k in ['success', 'warning', 'failed', 'running', 'canceled']" :key="k"><i :style="{ background: statusColor[k] }" />{{ statusText[k] }}</span>
    <span><i :style="{ background: c.failed }" />{{ $t('gantt.legendOverloaded') }}</span>
  </div>
  <div ref="el" class="gantt" />
</template>

<style scoped>
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  font-size: 12px;
  color: var(--bk-secondary);
  margin-bottom: 12px;
}
.legend i {
  display: inline-block;
  width: 12px;
  height: 10px;
  margin-right: 6px;
  border-radius: 2px;
  vertical-align: -1px;
}
.legend i.tick {
  width: 1px;
  height: 12px;
  background: var(--bk-on-surface);
}
.legend i.now {
  width: 0;
  height: 12px;
  border-left: 1px dashed var(--el-color-warning);
}
.cross {
  color: var(--bk-secondary);
  margin-right: 4px;
}
.gantt {
  width: 100%;
  min-height: 360px;
}
</style>
