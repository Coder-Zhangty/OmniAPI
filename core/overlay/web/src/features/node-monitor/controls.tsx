import { useMutation, useQueryClient } from '@tanstack/react-query'
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

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { submitOperation } from './api'
import type { Operation, Snapshot } from './types'

export function Controls({ snapshot }: { snapshot: Snapshot }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [weight, setWeight] = useState(
    String(snapshot.routing.configured.primary)
  )
  const [pending, setPending] = useState<{
    operation: Operation
    title: string
    key: string
    revision: number
  } | null>(null)
  const mutation = useMutation({
    mutationFn: () => {
      if (!pending) throw new Error('No operation selected')
      return submitOperation(pending.operation, pending.revision, pending.key)
    },
    onSuccess: () => {
      setPending(null)
      void client.invalidateQueries({ queryKey: ['node-monitor'] })
    },
  })
  const active = snapshot.operations.some(
    (job) => !['succeeded', 'failed', 'cancelled'].includes(job.state)
  )
  const choose = (operation: Operation, title: string) => {
    mutation.reset()
    setPending({
      operation,
      title,
      key: crypto.randomUUID(),
      revision: snapshot.routing.configured.revision,
    })
  }
  const desired = Number(weight)
  return (
    <section className='bg-card space-y-4 rounded-xl border p-5'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h2 className='font-semibold'>{t('Gateway operations')}</h2>
        <span className='text-muted-foreground text-xs'>
          {t('Configuration revision')} {snapshot.routing.configured.revision}
        </span>
      </div>
      <div className='flex flex-wrap items-center gap-3'>
        <label htmlFor='primary-weight' className='text-sm'>
          {t('Primary traffic percent')}
        </label>
        <Input
          id='primary-weight'
          className='w-20'
          type='number'
          min={0}
          max={100}
          value={weight}
          onChange={(event) => setWeight(event.target.value)}
        />
        <span className='text-muted-foreground text-sm'>
          {t('Relay node')} {100 - desired}%
        </span>
        <Button
          variant='outline'
          disabled={
            active || !weight.trim() || !Number.isInteger(desired) || desired < 0 || desired > 100
          }
          onClick={() =>
            choose(
              { action: 'set_weights', primary: desired, relay: 100 - desired },
              `${t('Adjust traffic weights')} · ${desired}% / ${100 - desired}%`
            )
          }
        >
          {t('Apply weights')}
        </Button>
        <Button
          variant='outline'
          disabled={active}
          onClick={() =>
            choose(
              {
                action: 'set_relay_enabled',
                enabled: !snapshot.routing.configured.relay_enabled,
              },
              t(
                snapshot.routing.configured.relay_enabled
                  ? 'Pause relay traffic'
                  : 'Resume relay traffic'
              )
            )
          }
        >
          {t(
            snapshot.routing.configured.relay_enabled
              ? 'Pause relay traffic'
              : 'Resume relay traffic'
          )}
        </Button>
        {(['primary', 'relay'] as const).map((node) => (
          <Button
            key={node}
            variant='outline'
            disabled={active}
            onClick={() =>
              choose(
                { action: 'restart_gateway', node },
                `${t('Restart gateway')} · ${t(node === 'primary' ? 'Primary node' : 'Relay node')}`
              )
            }
          >
            {t('Restart gateway')} ·{' '}
            {t(node === 'primary' ? 'Primary node' : 'Relay node')}
          </Button>
        ))}
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Restart drains all in-flight gateway requests for up to 5 minutes. A timeout cancels the restart. Primary-only services enter maintenance while the primary drains.'
        )}
      </p>
      {snapshot.operations.slice(0, 5).map((job) => (
        <div
          className='flex flex-wrap justify-between gap-2 border-t pt-3 text-sm'
          key={job.id}
        >
          <span>
            {t(job.action)} · {t(job.node ?? 'primary')} ·{' '}
            <span
              className={
                job.state === 'failed'
                  ? 'text-destructive'
                  : 'text-muted-foreground'
              }
            >
              {t(job.state)}
            </span>
          </span>
          <span className='text-muted-foreground'>
            {t(job.message)}
            {job.active_requests != null &&
              ` · ${t('In flight')} ${job.active_requests}`}
            {job.remaining_seconds != null && ` · ${job.remaining_seconds}s`}
          </span>
        </div>
      ))}
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) setPending(null)
        }}
        title={pending?.title ?? ''}
        desc={t(
          'This affects live model routing. Database replication and the SSH tunnel keep running. Configuration and actor will be audited.'
        )}
        handleConfirm={() => mutation.mutate()}
        isLoading={mutation.isPending}
        confirmText={t('Confirm operation')}
        destructive={pending?.operation.action === 'restart_gateway'}
      >
        {pending && (
          <p className='text-muted-foreground text-xs'>
            {t('Configuration revision')} {pending.revision} ·
            {' '}103.121.93.231 / 160.202.46.124
          </p>
        )}
        {mutation.error && (
          <p className='text-destructive text-sm' role='alert'>
            {mutation.error.message}
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}
