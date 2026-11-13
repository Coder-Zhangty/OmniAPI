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

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { bytes, date, number } from './format'
import type { MonitorNode, NodeID, Snapshot } from './types'

function DiskSeverity({
  severity,
}: {
  severity: 'maintenance' | 'critical' | 'warning'
}) {
  const { t } = useTranslation()
  let label = t('Business disk warning')
  if (severity === 'maintenance') label = t('Maintenance only')
  if (severity === 'critical') label = t('Critical business disk')
  return (
    <Badge variant={severity === 'maintenance' ? 'secondary' : 'destructive'}>
      {label}
    </Badge>
  )
}

function DiskRole({ role }: { role?: 'business' | 'maintenance' | 'display' }) {
  const { t } = useTranslation()
  let label = t('Filesystem')
  if (role === 'business') label = t('Business filesystem')
  if (role === 'maintenance') label = t('Maintenance only')
  return <Badge variant='secondary'>{label}</Badge>
}

export function State({
  value,
  label,
}: {
  value: boolean | null | undefined
  label: string
}) {
  const { t } = useTranslation()
  let status = t('Unknown')
  if (value === true) status = t('Healthy')
  if (value === false) status = t('Unavailable')
  return (
    <Badge
      variant='outline'
      className={value === false ? 'text-destructive' : ''}
    >
      {label} · {status}
    </Badge>
  )
}
function Meter({
  label,
  value,
  detail,
}: {
  label: string
  value: number | null | undefined
  detail?: string
}) {
  const { i18n } = useTranslation()
  return (
    <div className='space-y-2'>
      <div className='flex justify-between gap-3 text-sm'>
        <span>{label}</span>
        <span className='tabular-nums'>
          {number(value, i18n.language)}
          {value != null && '%'}
        </span>
      </div>
      <div
        className='bg-muted h-1.5 overflow-hidden rounded-full'
        role='meter'
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={value ?? undefined}
      >
        <div
          className={
            value != null && value >= 90
              ? 'bg-destructive h-full'
              : 'bg-primary h-full'
          }
          style={{ width: value == null ? 0 : `${Math.min(100, value)}%` }}
        />
      </div>
      {detail && <p className='text-muted-foreground text-xs'>{detail}</p>}
    </div>
  )
}
export function Topology({ snapshot }: { snapshot: Snapshot }) {
  const { t, i18n } = useTranslation()
  const { routing } = snapshot
  return (
    <section
      className='bg-card space-y-4 rounded-xl border p-5'
      aria-label={t('Node topology')}
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h2 className='font-semibold'>{t('Node topology')}</h2>
        <p className='text-muted-foreground text-xs'>
          {t('Collected at')} {date(snapshot.timestamp, i18n.language)} ·{' '}
          {t('Refreshes every 10 seconds')}
        </p>
      </div>
      <div className='flex flex-wrap gap-2'>
        <Badge variant={snapshot.overall_healthy ? 'secondary' : 'destructive'}>
          {t('Overall health')} ·{' '}
          {t(snapshot.overall_healthy ? 'Healthy' : 'Needs attention')}
        </Badge>
        <State
          value={Boolean(snapshot.health.durable)}
          label={t('Billing writable')}
        />
        <State
          value={
            typeof snapshot.health.tunnel === 'boolean'
              ? snapshot.health.tunnel
              : null
          }
          label={t('SSH tunnel')}
        />
        <State
          value={
            typeof snapshot.health.replication === 'boolean'
              ? snapshot.health.replication
              : null
          }
          label={t('Database replication')}
        />
        <Badge variant='outline'>
          {t('Model concurrency')} ·{' '}
          {number(snapshot.model_active, i18n.language)}
        </Badge>
      </div>
      <div className='grid items-center gap-4 sm:grid-cols-[1fr_auto_1fr]'>
        <div className='bg-muted/50 rounded-lg p-4'>
          <p className='font-medium'>
            {t('Primary node')}{' '}
            <span className='font-mono text-xs'>103.121.93.231</span>
          </p>
          <p className='text-muted-foreground text-sm'>
            {t('Configured')} {routing.configured.primary}% ·{' '}
            {t('Actual last hour')}{' '}
            {number(routing.actual?.primary, i18n.language)}%
          </p>
        </div>
        <div className='text-muted-foreground text-center' aria-hidden>
          ⇄
        </div>
        <div className='bg-muted/50 rounded-lg p-4'>
          <p className='font-medium'>
            {t('Relay node')}{' '}
            <span className='font-mono text-xs'>160.202.46.124</span>
          </p>
          <p className='text-muted-foreground text-sm'>
            {t('Configured')} {routing.configured.relay}% ·{' '}
            {t('Actual last hour')}{' '}
            {number(routing.actual?.relay, i18n.language)}%
          </p>
        </div>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t('Effective traffic pool')}:{' '}
        {routing.effective
          ? `${routing.effective.primary}% / ${routing.effective.relay}%`
          : t('Unavailable')}{' '}
        · {t('Main ingress carries public bandwidth')}
      </p>
      {!!snapshot.recent_exceptions?.length && (
        <div className='text-muted-foreground space-y-1 border-t pt-3 text-xs'>
          <p className='font-medium'>{t('Recent exceptions')}</p>
          {snapshot.recent_exceptions.map((event) => (
            <p key={event.id}>
              {date(event.timestamp, i18n.language)} · {t(event.node)} ·{' '}
              {event.title}
            </p>
          ))}
        </div>
      )}
      {!!snapshot.disk_issues?.length && (
        <div className='space-y-2 border-t pt-3 text-xs'>
          {snapshot.disk_issues.map((issue) => (
            <p
              key={`${issue.node}-${issue.severity}-${issue.detail}`}
              className='flex flex-wrap items-center gap-2'
            >
              <DiskSeverity severity={issue.severity} />
              <span>
                {t(issue.node)} · {issue.detail}
              </span>
            </p>
          ))}
        </div>
      )}
    </section>
  )
}
export function NodeCard({
  node,
  expanded,
  onSelect,
}: {
  node: MonitorNode
  expanded: boolean
  onSelect: (node: NodeID) => void
}) {
  const { t, i18n } = useTranslation()
  const m = node.metrics
  return (
    <article className='bg-card space-y-5 rounded-xl border p-5'>
      <div className='flex items-start justify-between gap-3'>
        <div>
          <h2 className='font-semibold'>
            {t(node.id === 'primary' ? 'Primary node' : 'Relay node')}
          </h2>
          <p className='text-muted-foreground mt-1 font-mono text-sm'>
            {node.ip}
          </p>
        </div>
        <Badge variant={node.fresh ? 'secondary' : 'destructive'}>
          {t(node.fresh ? 'Collecting' : 'Collector disconnected')}
        </Badge>
        {node.high_load && (
          <Badge variant='destructive'>{t('Sustained high load')}</Badge>
        )}
      </div>
      <State value={node.gateway_healthy} label={t('Gateway')} />
      <div className='grid grid-cols-2 gap-6'>
        <Meter
          label={`CPU · ${number(m?.cpu_cores, i18n.language)} ${t('cores')}`}
          value={m?.cpu_percent}
        />
        <Meter
          label={t('Memory')}
          value={m?.memory_percent}
          detail={`${bytes(m?.memory_used, i18n.language)} / ${bytes(m?.memory_total, i18n.language)}`}
        />
      </div>
      {m?.disks.map((disk) => (
        <div key={disk.device} className='space-y-2'>
          <div className='flex flex-wrap items-center gap-2 text-xs'>
            <DiskRole role={disk.role} />
            {!!disk.business_paths?.length && (
              <span className='text-muted-foreground break-all'>
                {disk.business_paths.join(' · ')}
              </span>
            )}
          </div>
          <Meter
            label={`${t('Disk')} · ${disk.mount}`}
            value={disk.percent}
            detail={`${bytes(disk.used, i18n.language)} / ${bytes(disk.total, i18n.language)} · ${t('Free')} ${bytes(disk.free, i18n.language)}`}
          />
        </div>
      ))}
      {!m && (
        <p className='text-muted-foreground text-sm'>
          {t('Metrics unavailable. Old values are hidden.')}
        </p>
      )}
      <div className='flex flex-wrap justify-between gap-3 text-sm'>
        <span>
          {t('Load per core')} · {number(m?.load_per_core, i18n.language)}
        </span>
        <Button variant='ghost' size='sm' onClick={() => onSelect(node.id)}>
          {t(expanded ? 'Hide details' : 'Node details')}
        </Button>
      </div>
      {expanded && (
        <dl className='grid grid-cols-2 gap-3 border-t pt-4 text-xs'>
          <dt>{t('Load 1 / 5 / 15 minutes')}</dt>
          <dd className='text-right'>
            {m?.load
              ?.map((value) => number(value, i18n.language))
              .join(' / ') ?? '—'}
          </dd>
          <dt>{t('Network receive / send')}</dt>
          <dd className='text-right'>
            {bytes(m?.network_rx, i18n.language)}/s /{' '}
            {bytes(m?.network_tx, i18n.language)}/s
          </dd>
          <dt>{t('Model concurrency')}</dt>
          <dd className='text-right'>
            {number(m?.model_active, i18n.language)}
          </dd>
          <dt>{t('Observer dropped events')}</dt>
          <dd className='text-right'>
            {number(m?.observer_dropped, i18n.language)}
          </dd>
          <dt>{t('Last collected')}</dt>
          <dd className='text-right'>{date(node.last_seen, i18n.language)}</dd>
        </dl>
      )}
    </article>
  )
}
