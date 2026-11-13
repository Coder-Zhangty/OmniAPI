/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { toIntlLocale } from '@/i18n/languages'

import type { ResourcePoint, Series } from './types'

export function Trends({ series }: { series?: Series }) {
  const { t, i18n } = useTranslation()
  const tick = (value: number) =>
    new Intl.DateTimeFormat(toIntlLocale(i18n.language), {
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(value * 1000)
  const resources = new Map<number, Record<string, number>>()
  for (const row of series?.resources ?? []) {
    const point = resources.get(row.timestamp) ?? { timestamp: row.timestamp }
    for (const metric of ['cpu_percent', 'memory_percent'] as const) {
      const value = row[metric]
      if (value != null) point[`${row.node}_${metric}`] = value
      const peak = row.peak[metric]
      if (peak != null) point[`${row.node}_${metric}_peak`] = peak
    }
    resources.set(row.timestamp, point)
  }
  const requestRows = new Map<number, Record<string, number>>()
  const latencyRows = new Map<number, Record<string, number>>()
  for (const row of series?.requests ?? []) {
    const point = requestRows.get(row.timestamp) ?? { timestamp: row.timestamp }
    point[row.node] = row.rpm
    requestRows.set(row.timestamp, point)
    const latency = latencyRows.get(row.timestamp) ?? { timestamp: row.timestamp }
    if (row.p50_ms != null) latency[`${row.node}_p50`] = row.p50_ms
    if (row.p95_ms != null) latency[`${row.node}_p95`] = row.p95_ms
    if (row.first_p95_ms != null) latency[`${row.node}_first`] = row.first_p95_ms
    latencyRows.set(row.timestamp, latency)
  }
  const chart = (
    title: string,
    data: Record<string, number>[],
    lines: { key: string; name: string; color: string; dash?: string }[],
    percent = false
  ) => (
    <section className='bg-card min-w-0 space-y-4 rounded-xl border p-5'>
      <h2 className='font-semibold'>{title}</h2>
      {data.length === 0 ? (
        <p className='text-muted-foreground flex h-56 items-center justify-center text-sm'>
          {t('No collected history in this period')}
        </p>
      ) : (
        <ChartContainer
          className='h-64 w-full'
          config={Object.fromEntries(
            lines.map((line) => [
              line.key,
              { label: line.name, color: line.color },
            ])
          )}
        >
          <LineChart accessibilityLayer data={data}>
            <CartesianGrid vertical={false} />
            <XAxis dataKey='timestamp' tickFormatter={tick} minTickGap={50} />
            <YAxis domain={percent ? [0, 100] : ['auto', 'auto']} width={40} />
            <ChartTooltip
              content={
                <ChartTooltipContent
                  labelFormatter={(label) => tick(Number(label))}
                />
              }
            />
            <ChartLegend content={<ChartLegendContent />} />
            {lines.map((line) => (
              <Line
                key={line.key}
                dataKey={line.key}
                name={line.name}
                stroke={line.color}
                strokeDasharray={line.dash}
                strokeWidth={line.dash ? 1 : 2}
                dot={false}
                connectNulls={false}
                isAnimationActive={false}
              />
            ))}
          </LineChart>
        </ChartContainer>
      )}
    </section>
  )
  const resourceLines = (metric: keyof ResourcePoint, label: string) =>
    ['primary', 'relay'].flatMap((node, i) => [
      {
        key: `${node}_${metric}`,
        name: `${t(node === 'primary' ? 'Primary node' : 'Relay node')} · ${label}`,
        color: `var(--chart-${i + 1})`,
      },
      {
        key: `${node}_${metric}_peak`,
        name: `${t(node === 'primary' ? 'Primary node' : 'Relay node')} · ${t('Peak')}`,
        color: `var(--chart-${i + 1})`,
        dash: '3 4',
      },
    ])
  return (
    <div className='grid gap-4 lg:grid-cols-2'>
      {chart(
        t('CPU average and peak'),
        [...resources.values()],
        resourceLines('cpu_percent', t('Average')),
        true
      )}
      {chart(
        t('Memory average and peak'),
        [...resources.values()],
        resourceLines('memory_percent', t('Average')),
        true
      )}
      {chart(
        t('Requests per minute'),
        [...requestRows.values()],
        [
          { key: 'primary', name: t('Primary node'), color: 'var(--chart-1)' },
          { key: 'relay', name: t('Relay node'), color: 'var(--chart-2)' },
          { key: 'unknown', name: t('Unknown node'), color: 'var(--chart-3)' },
        ]
      )}
      {chart(
        t('Latency P50 / P95'),
        [...latencyRows.values()],
        ['primary', 'relay'].flatMap((node, i) => [
          {
            key: `${node}_p50`,
            name: `${t(node === 'primary' ? 'Primary node' : 'Relay node')} P50 (ms)`,
            color: `var(--chart-${i + 1})`,
            dash: '3 4',
          },
          {
            key: `${node}_p95`,
            name: `${t(node === 'primary' ? 'Primary node' : 'Relay node')} P95 (ms)`,
            color: `var(--chart-${i + 1})`,
          },
          {
            key: `${node}_first`,
            name: `${t(node === 'primary' ? 'Primary node' : 'Relay node')} ${t('First token')} P95 (ms)`,
            color: `var(--chart-${i + 3})`,
          },
        ])
      )}
    </div>
  )
}
