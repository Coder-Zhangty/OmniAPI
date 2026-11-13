import { useQuery } from '@tanstack/react-query'
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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { readMonitor } from './api'
import { Trends } from './charts'
import { Controls } from './controls'
import { date, filterOptions } from './format'
import { EventHistory, RequestHistory, SummaryStrip } from './history'
import { NodeCard, Topology } from './nodes'
import { useFreshSnapshot } from './use-fresh-snapshot'
import type {
  MonitorEvent,
  NodeID,
  Outcome,
  Page,
  Params,
  RequestRow,
  Series,
  Snapshot,
} from './types'

const periods = [
  ['Last hour', 3600],
  ['Last 24 hours', 86400],
  ['Last 7 days', 604800],
  ['Last 30 days', 2592000],
  ['Last 90 days', 7776000],
] as const
export function NodeMonitor() {
  const { t, i18n } = useTranslation()
  const [period, setPeriod] = useState(3600)
  const [filters, setFilters] = useState({
    node: '',
    category: '',
    model: '',
    outcome: '',
  })
  const [custom, setCustom] = useState<{ start: number; end: number } | null>(
    null
  )
  const [dates, setDates] = useState({ start: '', end: '' })
  const [expanded, setExpanded] = useState<NodeID | null>(null)
  const [page, setPage] = useState(1)
  const [eventPage, setEventPage] = useState(1)
  const snapshot = useQuery({
    queryKey: ['node-monitor', 'snapshot'],
    queryFn: () => readMonitor<Snapshot>('snapshot'),
    refetchInterval: 10000,
  })
  // Resolve moving windows at fetch time, keeping cache keys stable between refreshes.
  const params = (): Params => ({
    ...filters,
    start: custom?.start ?? Date.now() / 1000 - period + 10,
    end: custom?.end ?? Date.now() / 1000,
  })
  const series = useQuery({
    queryKey: ['node-monitor', 'series', period, custom, filters],
    queryFn: () => readMonitor<Series>('series', params()),
    refetchInterval: 10000,
  })
  const requests = useQuery({
    queryKey: ['node-monitor', 'requests', period, custom, filters, page],
    queryFn: () =>
      readMonitor<Page<RequestRow>>('requests', { ...params(), page }),
    refetchInterval: 10000,
  })
  const events = useQuery({
    queryKey: ['node-monitor', 'events', eventPage],
    queryFn: () =>
      readMonitor<Page<MonitorEvent>>('events', { page: eventPage }),
    refetchInterval: 10000,
  })
  const changeFilter = (key: keyof typeof filters, value: string) => {
    setFilters({ ...filters, [key]: value })
    setPage(1)
  }
  const error = snapshot.error || series.error || requests.error || events.error
  const { data: live, stale } = useFreshSnapshot(snapshot.data)
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        <span className='inline-flex items-center gap-2'>
          {t('Node Monitor')}
          <Badge variant='outline'>Root</Badge>
        </span>
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-5 pb-6'>
          {(error || stale) && (
            <div
              role='alert'
              className='border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-4 text-sm'
            >
              {stale ? t('Collector disconnected') : error?.message}
              <Button
                variant='ghost'
                size='sm'
                onClick={() => {
                  void snapshot.refetch()
                  void series.refetch()
                }}
              >
                {t('Retry')}
              </Button>
            </div>
          )}
          {!snapshot.data && (
            <p className='text-muted-foreground py-16 text-center'>
              {t(
                snapshot.isPending
                  ? 'Loading node metrics'
                  : 'Monitor is unavailable'
              )}
            </p>
          )}
          {live && !stale && (
            <>
              <Topology snapshot={live} />
              <div className='grid gap-4 lg:grid-cols-2'>
                {live.nodes.map((node) => (
                  <NodeCard
                    key={node.id}
                    node={node}
                    expanded={expanded === node.id}
                    onSelect={(id) => setExpanded(expanded === id ? null : id)}
                  />
                ))}
              </div>
              <Controls snapshot={live} />
            </>
          )}
          <section aria-label={t('History filters')} className='space-y-3'>
            <div className='flex flex-wrap gap-2'>
              {periods.map(([label, seconds]) => (
                <Button
                  key={seconds}
                  size='sm'
                  variant={
                    period === seconds && !custom ? 'default' : 'outline'
                  }
                  onClick={() => {
                    setPeriod(seconds)
                    setCustom(null)
                    setPage(1)
                  }}
                >
                  {t(label)}
                </Button>
              ))}
            </div>
            <div className='flex flex-wrap items-end gap-3'>
              {(['node', 'category', 'outcome'] as const).map((key) => (
                <label
                  className='text-muted-foreground space-y-1 text-xs'
                  key={key}
                >
                  <span>{t(key)}</span>
                  <select
                    className='bg-background text-foreground block h-9 rounded-md border px-3 text-sm'
                    value={filters[key]}
                    onChange={(event) => changeFilter(key, event.target.value)}
                  >
                    <option value=''>{t('All')}</option>
                    {filterOptions(key).map((value) => (
                      <option key={value} value={value}>
                        {t(value)}
                      </option>
                    ))}
                  </select>
                </label>
              ))}
              <label className='text-muted-foreground space-y-1 text-xs'>
                <span>{t('Model')}</span>
                <Input
                  className='w-44'
                  value={filters.model}
                  onChange={(event) =>
                    changeFilter('model', event.target.value)
                  }
                />
              </label>
              <label className='text-muted-foreground space-y-1 text-xs'>
                <span>{t('Start time')}</span>
                <Input
                  aria-label={t('Start time')}
                  type='datetime-local'
                  value={dates.start}
                  onChange={(event) =>
                    setDates({ ...dates, start: event.target.value })
                  }
                />
              </label>
              <label className='text-muted-foreground space-y-1 text-xs'>
                <span>{t('End time')}</span>
                <Input
                  aria-label={t('End time')}
                  type='datetime-local'
                  value={dates.end}
                  onChange={(event) =>
                    setDates({ ...dates, end: event.target.value })
                  }
                />
              </label>
              <Button
                variant='outline'
                size='sm'
                disabled={
                  !dates.start ||
                  !dates.end ||
                  new Date(dates.start) >= new Date(dates.end) ||
                  Date.now() - new Date(dates.start).getTime() > 7776000000
                }
                onClick={() => {
                  setCustom({
                    start: new Date(dates.start).getTime() / 1000,
                    end: new Date(dates.end).getTime() / 1000,
                  })
                  setPage(1)
                }}
              >
                {t('Apply custom range')}
              </Button>
            </div>
          </section>
          <SummaryStrip summary={series.data?.summary} />
          <div className='text-muted-foreground flex flex-wrap gap-4 text-sm'>
            {(
              [
                'success',
                'failure',
                'rejected',
                'cancelled',
                'edge_rejected',
              ] satisfies Outcome[]
            ).map((result) => (
              <span key={result}>
                {t(result)} · {series.data?.summary[result] ?? '—'}
              </span>
            ))}
          </div>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Latency uses merged histograms; percentiles are approximate bucket bounds. History starts when monitoring is enabled.'
            )}{' '}
            {snapshot.data?.started_at &&
              date(snapshot.data.started_at, i18n.language)}
          </p>
          <Trends series={series.data} />
          <RequestHistory data={requests.data} page={page} onPage={setPage} />
          <EventHistory
            data={events.data}
            page={eventPage}
            onPage={setEventPage}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
