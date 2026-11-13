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
import { Activity, BarChart3, WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { formatQuota } from '@/lib/format'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { UserWalletData } from '../types'

interface WalletStatsCardProps {
  user: UserWalletData | null
  loading?: boolean
}

export function WalletStatsCard(props: WalletStatsCardProps) {
  const { t } = useTranslation()
  const current = useQuery({
    queryKey: ['wallet', 'balance-snapshot', props.user?.id],
    enabled: Boolean(props.user?.id),
    queryFn: async ({ signal }) => requireServerSuccess((await api.get('/api/user/self', { signal, disableDuplicate: true, timeout: 10000 })).data).data as UserWalletData,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  })
  const user = current.data ?? props.user
  if (props.loading) {
    return (
      <div className='grid grid-cols-3 divide-x rounded-lg border'>
        {['balance', 'usage', 'requests'].map((key) => (
          <div key={key} className='min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4'>
            <Skeleton className='h-3.5 w-full' />
            <Skeleton className='mt-2 h-6 w-full sm:h-7' />
            <Skeleton className='mt-1.5 hidden h-3.5 w-24 md:block' />
          </div>
        ))}
      </div>
    )
  }

  const stats: {
    label: string
    value: string
    description: string
    icon: typeof WalletCards
    tone: IconBadgeTone
  }[] = [
    {
      label: user?.wallet_balance_quota === undefined ? t('Available balance') : t('Wallet balance'),
      value: formatQuota(user?.wallet_balance_quota ?? user?.quota ?? 0),
      description: t('Only actual settled usage reduces this balance'),
      icon: WalletCards,
      tone: 'success',
    },
    {
      label: t('Total Usage'),
      value: formatQuota(user?.used_quota ?? 0),
      description: t('Total consumed quota'),
      icon: BarChart3,
      tone: 'info',
    },
    {
      label: t('API Requests'),
      value: (user?.request_count ?? 0).toLocaleString(),
      description: t('Total requests made'),
      icon: Activity,
      tone: 'chart-4',
    },
  ]

  return (
    <div>
    <div className='grid grid-cols-3 divide-x rounded-lg border'>
      {stats.map((item) => (
        <div key={item.label} className='min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4'>
          <div className='flex items-center gap-1.5 sm:gap-2.5'>
            <IconBadge tone={item.tone} size='stat'>
              <item.icon />
            </IconBadge>
            <div className='text-muted-foreground truncate text-[11px] font-medium tracking-wider uppercase sm:text-xs'>
              {item.label}
            </div>
          </div>

          <div className='text-foreground mt-1.5 font-mono text-sm font-bold tracking-tight break-all tabular-nums sm:mt-2.5 sm:text-2xl'>
            {item.value}
          </div>
          <div className='text-muted-foreground/60 mt-1 hidden text-xs md:block'>
            {item.description}
          </div>
        </div>
      ))}
    </div>
    {user?.wallet_reserved_quota !== undefined && <p className='text-muted-foreground mt-2 text-xs leading-relaxed'>
      {t('Available balance')}: {formatQuota(user.quota)} · {t('Temporary reservation')}: {formatQuota(user.wallet_reserved_quota)}。{t('Reservations are released or replaced by actual usage after each request.')}
    </p>}
    {current.isError && <p role='status' className='mt-2 text-xs text-amber-700'>{t('Could not refresh balance. Displayed values may be outdated.')}</p>}
    </div>
  )
}
