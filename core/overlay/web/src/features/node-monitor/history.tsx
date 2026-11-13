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

import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { date, number, nodeLabel } from './format'
import type { MonitorEvent, Page, RequestRow, Summary } from './types'

export function SummaryStrip({ summary }: { summary?: Summary }) {
  const { t, i18n } = useTranslation()
  const items: [string, number | null | undefined, string][] = [
    ['Client requests', summary?.total, ''],
    ['Success rate', summary?.success_rate, '%'],
    ['First token P50', summary?.first_p50_ms, 'ms'],
    ['First token P95', summary?.first_p95_ms, 'ms'],
    ['Total latency P50', summary?.p50_ms, 'ms'],
    ['Total latency P95', summary?.p95_ms, 'ms'],
  ]
  return (
    <div className='bg-card grid grid-cols-2 divide-y rounded-xl border sm:divide-x sm:divide-y-0 md:grid-cols-3 xl:grid-cols-6'>
      {items.map(([label, value, suffix]) => (
        <div key={label} className='p-4'>
          <p className='text-muted-foreground text-xs'>{t(label)}</p>
          <p className='mt-2 text-xl font-semibold tabular-nums'>
            {number(value, i18n.language)}{' '}
            <span className='text-xs font-normal'>
              {value != null && suffix}
            </span>
          </p>
        </div>
      ))}
    </div>
  )
}
export function RequestHistory({
  data,
  page,
  onPage,
}: {
  data?: Page<RequestRow>
  page: number
  onPage: (page: number) => void
}) {
  const { t, i18n } = useTranslation()
  return (
    <section className='bg-card overflow-hidden rounded-xl border'>
      <div className='flex flex-wrap justify-between gap-2 p-5'>
        <h2 className='font-semibold'>{t('Request summaries')}</h2>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Summaries retained for 7 days. No chat content or keys are collected.'
          )}
        </p>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            {[
              'Time',
              'Node',
              'Business category',
              'Model',
              'Result',
              'Total latency',
              'First token',
              'Upstream attempts',
              'Channel chain',
              'Request ID',
            ].map((label) => (
              <TableHead key={label}>{t(label)}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.items.map((row) => (
            <TableRow key={row.edge_id || row.event_id}>
              <TableCell className='whitespace-nowrap'>
                {date(row.timestamp / 1000, i18n.language)}
              </TableCell>
              <TableCell>{t(nodeLabel(row.node))}</TableCell>
              <TableCell>{t(row.category)}</TableCell>
              <TableCell className='max-w-40 truncate' title={row.model}>
                {row.model || '—'}
              </TableCell>
              <TableCell
                className={row.outcome === 'failure' ? 'text-destructive' : ''}
              >
                {t(row.outcome)}
              </TableCell>
              <TableCell>{number(row.duration_ms, i18n.language)}ms</TableCell>
              <TableCell>
                {number(row.first_ms, i18n.language)}
                {row.first_ms != null && 'ms'}
              </TableCell>
              <TableCell>{number(row.attempts, i18n.language)}</TableCell>
              <TableCell>{row.chain?.join(' → ') || '—'}</TableCell>
              <TableCell
                className='max-w-44 truncate font-mono'
                title={row.request_id || row.edge_id}
              >
                {row.request_id || row.edge_id}
              </TableCell>
            </TableRow>
          ))}
          {!data?.items.length && (
            <TableRow>
              <TableCell
                colSpan={10}
                className='text-muted-foreground h-24 text-center'
              >
                {t('No collected requests in this period')}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      <div className='flex flex-wrap items-center justify-between gap-3 border-t p-4'>
        <a href='/usage-logs/common' className='text-primary text-sm underline'>
          {t('Open business logs')}
        </a>
        <div className='flex items-center gap-3 text-sm'>
          <Button
            variant='outline'
            size='sm'
            disabled={page <= 1}
            onClick={() => onPage(page - 1)}
          >
            {t('Previous')}
          </Button>
          <span>
            {page} · {number(data?.total, i18n.language)}
          </span>
          <Button
            variant='outline'
            size='sm'
            disabled={!data || page * 25 >= data.total}
            onClick={() => onPage(page + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
    </section>
  )
}
export function EventHistory({
  data,
  page,
  onPage,
}: {
  data?: Page<MonitorEvent>
  page: number
  onPage: (page: number) => void
}) {
  const { t, i18n } = useTranslation()
  return (
    <section className='bg-card space-y-4 rounded-xl border p-5'>
      <h2 className='font-semibold'>{t('Alerts and audit events')}</h2>
      {data?.items.map((event) => (
        <div
          key={event.id}
          className='flex flex-wrap justify-between gap-2 border-t pt-3 text-sm'
        >
          <div>
            <p className='font-medium'>
              {t(event.title || event.action || event.kind)} · {t(event.node)}
            </p>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(event.detail || event.message || event.status || '')}
            </p>
          </div>
          <time className='text-muted-foreground text-xs'>
            {date(event.timestamp, i18n.language)}
          </time>
        </div>
      ))}
      {!data?.items.length && (
        <p className='text-muted-foreground text-sm'>{t('No events yet')}</p>
      )}
      <div className='flex justify-end gap-3'>
        <Button
          size='sm'
          variant='outline'
          disabled={page <= 1}
          onClick={() => onPage(page - 1)}
        >
          {t('Previous')}
        </Button>
        <Button
          size='sm'
          variant='outline'
          disabled={!data || page * 50 >= data.total}
          onClick={() => onPage(page + 1)}
        >
          {t('Next')}
        </Button>
      </div>
    </section>
  )
}
