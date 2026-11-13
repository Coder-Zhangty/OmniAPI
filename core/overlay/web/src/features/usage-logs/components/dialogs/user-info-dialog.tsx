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
import { Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuota, formatCompactNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getUserInfo } from '../../api'
import type { UserInfo } from '../../types'

export function AdminAllowanceInfo(props: {
  billing: NonNullable<UserInfo['billing']>
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const billing = props.billing
  const time = (timestamp: number) =>
    new Date(timestamp * 1000).toLocaleString(locale, { hour12: false })
  const allowance = (quota: number) => {
    if (billing.quota_per_unit <= 0 || billing.fx_micros <= 0) return '—'
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency: 'CNY',
      minimumFractionDigits: 2,
      maximumFractionDigits: 4,
    }).format(((quota / billing.quota_per_unit) * billing.fx_micros) / 1000000)
  }
  const names: Record<string, string> = {
    starter: t('Starter'),
    standard: t('Standard'),
    pro: t('Professional'),
  }
  let status = t('Expired')
  if (billing.status === 'active') status = t('Active')
  if (billing.status === 'processing')
    {status = t('Subscription activation pending')}
  if (billing.status === 'none')
    {return (
      <p className='text-muted-foreground text-xs'>
        {t('No dual-window subscription record')}
      </p>
    )}
  return (
    <section
      className='space-y-3 rounded-lg border p-3'
      aria-label={t('Subscription allowance')}
    >
      <div className='flex flex-wrap justify-between gap-2'>
        <h3 className='text-sm font-semibold'>
          {t('Current dual-window subscription')} ·{' '}
          {names[billing.plan_key] || billing.plan_key}
        </h3>
        <span className='text-sm'>{status}</span>
      </div>
      {billing.start > 0 && (
        <p className='text-muted-foreground text-xs'>
          {t('Start')}: {time(billing.start)} · {t('End')}: {time(billing.end)}
        </p>
      )}
      {billing.pending_plan && (
        <p className='text-xs'>
          {t('Next subscription plan')}:{' '}
          {names[billing.pending_plan] || billing.pending_plan}
        </p>
      )}
      {billing.windows.map((window) => (
        <div key={window.key} className='space-y-1'>
          <div className='flex flex-wrap justify-between gap-2 text-sm'>
            <span>
              {t(
                window.key === 'five'
                  ? 'Five-hour allowance'
                  : 'Weekly allowance'
              )}
            </span>
            <strong>
              {t('Remaining')}: {allowance(window.remaining)}
            </strong>
          </div>
          <p className='text-muted-foreground text-xs'>
            {t('Used')}: {allowance(window.used)} / {allowance(window.limit)} ·{' '}
            {t('Resets at')}: {time(window.reset_at)}
          </p>
          {window.paused && (
            <p className='text-xs text-amber-700'>
              {t('Subscription paused until this window resets')}
            </p>
          )}
        </div>
      ))}
      {!!billing.windows.length && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Allowance usage includes pending reservations and is corrected after settlement.'
          )}
        </p>
      )}
      {billing.wallet_overflow !== null && (
        <p className='text-xs'>
          {t('Wallet continuation')}:{' '}
          {t(billing.wallet_overflow ? 'Enabled' : 'Disabled')}
        </p>
      )}
    </section>
  )
}

interface UserInfoDialogProps {
  userId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

function InfoItem({ label, value }: { label: string; value: string | number }) {
  return (
    <div className='min-w-0 space-y-1.5'>
      <Label className='text-muted-foreground text-xs'>{label}</Label>
      <div className='text-sm font-semibold break-words tabular-nums'>
        {value}
      </div>
    </div>
  )
}

export function UserInfoDialog({
  userId,
  open,
  onOpenChange,
}: UserInfoDialogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [visible, setVisible] = useState(document.visibilityState === 'visible')
  const query = useQuery({
    queryKey: ['omni', 'admin-user-info', userId],
    enabled: open && Boolean(userId),
    queryFn: async ({ signal }) => {
      if (!userId) throw new Error('Missing user ID')
      return requireServerSuccess(await getUserInfo(userId, signal)).data
    },
    staleTime: 0,
    retry: false,
    refetchInterval: open && visible ? 5000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  })
  useEffect(() => {
    const update = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])
  const userInfo = query.data
  const isLoading = query.isPending && query.fetchStatus !== 'idle'
  const billing = userInfo?.billing
  const time = (timestamp: number) =>
    new Date(timestamp * 1000).toLocaleString(locale, { hour12: false })

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('User Information')}
      description={t(
        'View detailed information about this user including balance, usage statistics, and invitation details.'
      )}
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <div className='flex flex-wrap items-center justify-between gap-2 text-xs'>
        <p className='text-muted-foreground' role='status'>
          {t('Refreshes every 5 seconds while open')}
          {query.dataUpdatedAt > 0 && (
            <>
              {' '}
              · {t('Last updated')}: {time(query.dataUpdatedAt / 1000)}
            </>
          )}
        </p>
        <Button
          variant='outline'
          size='sm'
          disabled={query.isFetching || !userId}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </div>
      {query.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            'Could not refresh user information. Displayed values may be outdated.'
          )}
        </p>
      )}
      {isLoading && (
        <div className='flex items-center justify-center py-8'>
          <Loader2 className='text-muted-foreground size-6 animate-spin' />
        </div>
      )}
      {!isLoading && userInfo && (
        <div className='space-y-4 py-4'>
          {/* Basic Info */}
          <div className='grid grid-cols-2 gap-4'>
            <InfoItem label={t('Username')} value={userInfo.username} />
            {userInfo.display_name && (
              <InfoItem
                label={t('Display Name')}
                value={userInfo.display_name}
              />
            )}
          </div>

          {/* Balance Info */}
          <div className='grid grid-cols-2 gap-4'>
            <InfoItem
              label={t('Wallet balance')}
              value={formatQuota(userInfo.quota)}
            />
            <InfoItem
              label={t('Total API usage')}
              value={formatQuota(userInfo.used_quota)}
            />
          </div>
          <p className='text-muted-foreground text-xs leading-relaxed'>
            {t(
              'Subscription usage consumes subscription allowance, not wallet balance. Total API usage includes all funding sources.'
            )}
          </p>
          {billing && <AdminAllowanceInfo billing={billing} />}

          {/* Statistics */}
          <div className='grid grid-cols-2 gap-4'>
            <InfoItem
              label={t('Request Count')}
              value={formatCompactNumber(userInfo.request_count)}
            />
            {userInfo.group && (
              <InfoItem label={t('User Group')} value={userInfo.group} />
            )}
          </div>

          {/* Invitation Info */}
          {(userInfo.aff_code ||
            userInfo.aff_count !== undefined ||
            (userInfo.aff_quota !== undefined && userInfo.aff_quota > 0)) && (
            <>
              <div className='grid grid-cols-2 gap-4'>
                {userInfo.aff_code && (
                  <InfoItem
                    label={t('Invitation Code')}
                    value={userInfo.aff_code}
                  />
                )}
                {userInfo.aff_count !== undefined && (
                  <InfoItem
                    label={t('Invited Users')}
                    value={formatCompactNumber(userInfo.aff_count)}
                  />
                )}
              </div>

              {userInfo.aff_quota !== undefined && userInfo.aff_quota > 0 && (
                <InfoItem
                  label={t('Invitation Quota')}
                  value={formatQuota(userInfo.aff_quota)}
                />
              )}
            </>
          )}

          {/* Remark */}
          {userInfo.remark && (
            <div className='space-y-1.5'>
              <Label className='text-muted-foreground text-xs'>
                {t('Remark')}
              </Label>
              <div className='text-sm leading-relaxed font-semibold break-words'>
                {userInfo.remark}
              </div>
            </div>
          )}
        </div>
      )}
      {!isLoading && !userInfo && (
        <div className='text-muted-foreground py-8 text-center text-sm'>
          {t('No user information available')}
        </div>
      )}
    </Dialog>
  )
}
