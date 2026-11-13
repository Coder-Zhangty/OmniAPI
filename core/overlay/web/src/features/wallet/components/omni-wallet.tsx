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
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import {
  requireServerSuccess,
  getServerErrorMessage,
  getServerErrorSources,
  getServerErrorStatus,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

type Summary = {
  status: 'active' | 'processing' | 'expired' | 'none'
  server_time: number
  quota_per_unit: number
  membership_enabled: boolean
  account: { wallet_overflow: boolean }
  subscription: {
    plan_key: string
    start: number
    end: number
    fx_micros: number
    last_purchase_months: number
    months_purchased: number
    pending_plan?: string
  }
  windows: {
    enabled?: boolean
    key: string
    limit: number
    used: number
    remaining: number
    reset_at: number
  }[]
  paused_legacy: {
    title: string
    remaining_quota: number
    remaining_seconds: number
  }[]
  active_legacy?: { title: string; end: number }[]
  cards: { id: string; kind: string; state: string; expires: number }[]
  notices: { key: string; title: string; body: string }[]
}
function QuotaGauge({
  remaining,
  limit,
}: {
  remaining: number
  limit: number
}) {
  const fraction = limit > 0 ? Math.min(1, Math.max(0, remaining / limit)) : 0
  const percent = Math.round(fraction * 100)
  return (
    <div
      role='meter'
      aria-label='额度剩余比例'
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      aria-valuetext={`剩余 ${percent}%`}
      className='mx-auto w-full max-w-[280px] text-emerald-700 dark:text-emerald-400'
    >
      <svg
        viewBox='0 0 280 155'
        aria-hidden='true'
        className='w-full overflow-visible'
      >
        <path
          d='M 30 128 A 110 110 0 0 1 250 128'
          fill='none'
          stroke='currentColor'
          strokeOpacity='.12'
          strokeWidth='12'
          strokeLinecap='round'
        />
        <path
          d='M 30 128 A 110 110 0 0 1 250 128'
          fill='none'
          stroke='currentColor'
          strokeWidth='12'
          strokeLinecap='round'
          pathLength='100'
          strokeDasharray={`${fraction * 100} 100`}
          className='transition-[stroke-dasharray] duration-500 motion-reduce:transition-none'
        />
        {Array.from({ length: 11 }, (_, i) => {
          const a = (Math.PI * i) / 10
          return (
            <line
              key={i}
              x1={140 - 93 * Math.cos(a)}
              y1={128 - 93 * Math.sin(a)}
              x2={140 - (i % 5 === 0 ? 80 : 86) * Math.cos(a)}
              y2={128 - (i % 5 === 0 ? 80 : 86) * Math.sin(a)}
              stroke='currentColor'
              strokeOpacity={i % 5 === 0 ? '.6' : '.28'}
              strokeWidth={i % 5 === 0 ? 2 : 1}
            />
          )
        })}
        <g
          style={{
            transform: `rotate(${fraction * 180}deg)`,
            transformOrigin: '140px 128px',
          }}
          className='transition-transform duration-500 motion-reduce:transition-none'
        >
          <path d='M 140 124 L 57 128 L 140 132 Z' fill='currentColor' />
        </g>
        <circle cx='140' cy='128' r='7' fill='currentColor' />
        <text
          x='30'
          y='151'
          textAnchor='middle'
          fill='currentColor'
          opacity='.6'
          fontSize='11'
        >
          0
        </text>
        <text
          x='140'
          y='9'
          textAnchor='middle'
          fill='currentColor'
          opacity='.6'
          fontSize='11'
        >
          50
        </text>
        <text
          x='250'
          y='151'
          textAnchor='middle'
          fill='currentColor'
          opacity='.6'
          fontSize='11'
        >
          100
        </text>
      </svg>
      <p className='mt-1 text-center text-4xl font-semibold tracking-tight tabular-nums'>
        {percent}%
        <span className='text-muted-foreground ml-2 text-sm font-normal'>
          剩余
        </span>
      </p>
    </div>
  )
}
export function OmniWallet() {
  const { t } = useTranslation()
  const uid = useAuthStore((s) => s.auth.user?.id),
    client = useQueryClient()
  const [now, setNow] = useState(Date.now() / 1000),
    [visible, setVisible] = useState(document.visibilityState === 'visible')
  const [preferenceState, setPreferenceState] = useState<
    'idle' | 'saving' | 'verifying' | 'effective' | 'failed' | 'unknown'
  >('idle')
  const [preferenceError, setPreferenceError] = useState('')
  const saving = useRef(false),
    channel = useRef<BroadcastChannel | null>(null),
    syncPending = useRef(false)
  const currentUid = useRef(uid),
    mounted = useRef(true)
  currentUid.current = uid
  useEffect(() => {
    mounted.current = true
    saving.current = false
    syncPending.current = false
    setPreferenceState('idle')
    setPreferenceError('')
    return () => {
      mounted.current = false
    }
  }, [uid])
  const queryKey = ['omni', 'wallet', uid]
  const query = useQuery({
    queryKey,
    enabled: Boolean(uid),
    queryFn: async ({ signal }) =>
      requireServerSuccess(
        (
          await api.get('/api/omni/self', {
            signal,
            disableDuplicate: true,
            timeout: 10000,
          })
        ).data
      ).data as Summary,
    refetchInterval: visible && !saving.current ? 30000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: !saving.current,
  })
  const mutate = useMutation({
    meta: { errorToast: false },
    retry: false,
    mutationFn: async (body: { wallet_overflow: boolean }) => {
      saving.current = true
      setPreferenceError('')
      setPreferenceState('saving')
      await client.cancelQueries({ queryKey, exact: true })
      try {
        const response = requireServerSuccess(
          (await api.post('/api/omni/preferences', body, { timeout: 10000 }))
            .data
        )
        if (typeof response.data?.wallet_overflow !== 'boolean') {
          throw new Error('Missing authoritative preference')
        }
        return response.data as { wallet_overflow: boolean }
      } catch (error) {
        const status = getServerErrorStatus(error)
        const rejected =
          getServerErrorSources(error).some(
            (source) => source.success === false
          ) ||
          (status !== undefined &&
            status >= 400 &&
            status < 500 &&
            status !== 408)
        if (rejected) throw error
        if (!mounted.current || currentUid.current !== uid) throw error
        setPreferenceState('verifying')
        try {
          const verified = await client.fetchQuery<Summary>({
            queryKey,
            queryFn: async ({ signal }) =>
              requireServerSuccess(
                (
                  await api.get('/api/omni/self', {
                    signal,
                    disableDuplicate: true,
                    timeout: 10000,
                  })
                ).data
              ).data as Summary,
            staleTime: 0,
            retry: false,
          })
          if (verified.account.wallet_overflow === body.wallet_overflow) {
            return verified.account
          }
        } catch {
          setPreferenceState('unknown')
          throw new Error(
            t('Save result could not be verified. Reload before trying again.')
          )
        }
        setPreferenceState('unknown')
        throw new Error(
          t(
            'Current setting verified, but this change is not confirmed. Reload before trying again.'
          )
        )
      }
    },
    onSuccess: async (preference) => {
      await client.cancelQueries({ queryKey, exact: true })
      client.setQueryData<Summary>(queryKey, (current) =>
        current
          ? {
              ...current,
              account: {
                ...current.account,
                wallet_overflow: preference.wallet_overflow,
              },
            }
          : current
      )
      if (!mounted.current || currentUid.current !== uid) return
      setPreferenceState('effective')
      channel.current?.postMessage({ type: 'wallet-preference-changed', uid })
    },
    onError: (error) => {
      if (!mounted.current || currentUid.current !== uid) return
      setPreferenceState((current) =>
        current === 'unknown' ? current : 'failed'
      )
      setPreferenceError(
        getServerErrorMessage(
          error,
          t('Could not save setting. Please try again.')
        )
      )
    },
    onSettled: () => {
      if (!mounted.current || currentUid.current !== uid) return
      saving.current = false
      if (syncPending.current) {
        syncPending.current = false
        void client.invalidateQueries({ queryKey, exact: true })
      }
    },
  })
  useEffect(() => {
    if (!uid || typeof BroadcastChannel === 'undefined') return
    const connection = new BroadcastChannel(`omni-wallet-preference:${uid}`)
    channel.current = connection
    connection.onmessage = (event: MessageEvent<unknown>) => {
      if (!event.data || typeof event.data !== 'object') return
      const message = event.data as { type?: string; uid?: number }
      if (message.type !== 'wallet-preference-changed' || message.uid !== uid) {
        return
      }
      if (saving.current) {
        syncPending.current = true
        return
      }
      setPreferenceState('idle')
      void client
        .cancelQueries({ queryKey: ['omni', 'wallet', uid], exact: true })
        .then(() =>
          client.invalidateQueries({
            queryKey: ['omni', 'wallet', uid],
            exact: true,
          })
        )
        .catch(() => {})
    }
    return () => {
      connection.close()
      channel.current = null
    }
  }, [uid, client])
  const notice = useMutation({
    mutationFn: async (key: string) =>
      requireServerSuccess(
        (await api.post('/api/omni/notices/read', { key })).data
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['omni', 'wallet'] }),
  })
  const card = useMutation({
    mutationFn: async (id: string) =>
      requireServerSuccess(
        (await api.post('/api/omni/cards/redeem', { code: id })).data
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['omni', 'wallet'] }),
  })
  const refetch = query.refetch
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now() / 1000), 1000)
    const focus = () => {
      setVisible(document.visibilityState === 'visible')
      if (document.visibilityState === 'visible' && !saving.current) {
        void refetch()
      }
    }
    document.addEventListener('visibilitychange', focus)
    window.addEventListener('pageshow', focus)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', focus)
      window.removeEventListener('pageshow', focus)
    }
  }, [refetch])
  const d = query.data,
    time = now + (d ? d.server_time - query.dataUpdatedAt / 1000 : 0)
  const weekExhausted =
    d?.windows.some((w) => w.key === 'week' && w.remaining <= 0) || false
  const displayWindows =
    d?.windows.filter((w)=>w.enabled!==false).map((w) =>
      w.key === 'five' && weekExhausted ? { ...w, remaining: 0 } : w
    ) || []
  useEffect(() => {
    if (!d || d.status !== 'active' || !visible || saving.current) return
    const reset = Math.min(...d.windows.map((w) => w.reset_at))
    const timer = window.setTimeout(
      () => {
        if (!saving.current) void refetch()
      },
      Math.max(1000, (reset - time) * 1000 + 250)
    )
    return () => clearTimeout(timer)
  }, [d, visible, preferenceState, time, refetch])
  const date = (n: number) =>
    new Date(n * 1000).toLocaleString('zh-CN', { hour12: false })
  const names: Record<string, string> = {
    starter: '体验',
    standard: '标准',
    pro: '专业',
 v3_19:'19元套餐',v3_39:'39元套餐',v3_69:'69元套餐',v3_99:'99元套餐',v3_199:'199元套餐',
  }
  const amount = (q: number) => {
    const n = d
      ? ((q / d.quota_per_unit) * d.subscription.fx_micros) / 1000000
      : 0
    return n > 0 && n < 0.0001
      ? '小于 ¥0.0001'
      : new Intl.NumberFormat('zh-CN', {
          style: 'currency',
          currency: 'CNY',
          minimumFractionDigits: 2,
          maximumFractionDigits: n > 0 && n < 0.01 ? 4 : 2,
        }).format(n)
  }
  const countdown = (n: number) => {
    const sec = Math.max(0, Math.ceil(n - time))
    return sec === 0
      ? '正在更新额度…'
      : `${Math.floor(sec / 3600)}小时 ${Math.floor((sec % 3600) / 60)}分后重置`
  }
  const cards =
    d?.cards.filter((c) => c.state === 'unused' && c.expires > time) || []
  let subscriptionTitle = '尚未开通订阅',
    subscriptionDetail = '调用按当前套餐或钱包计费'
  if (d?.status === 'active') {
    subscriptionTitle = `${names[d.subscription.plan_key] || '订阅'} · ${({1:'月付',3:'季付',6:'半年付',12:'年付'} as Record<number,string>)[d.subscription.last_purchase_months || d.subscription.months_purchased] || '预付'}`
    subscriptionDetail = `到期：${date(d.subscription.end)}`
  } else if (d?.status === 'processing') {
    subscriptionTitle = '订阅生效处理中'
    subscriptionDetail = '正在完成原请求结算，随后立即开通'
  } else if (d?.status === 'expired') {
    subscriptionTitle = '订阅已到期'
  } else if (d?.active_legacy?.length) {
    subscriptionTitle = '当前使用历史套餐'
  }
  return (
    <section
      className='bg-background space-y-6 rounded-xl border p-5 sm:p-6'
      aria-label='订阅与用量'
    >
      <div className='flex flex-wrap items-start justify-between gap-4'>
        <div>
          <h2 className='text-lg font-semibold'>订阅与用量</h2>
          <p className='text-muted-foreground mt-1 text-sm'>
            API 按量付费扣钱包余额；订阅按当前套餐的适用窗口消耗额度。
          </p>
          <p className='text-muted-foreground mt-1 text-xs'>
            订阅仅用钱包余额购买，扫码仅用于充值钱包。
          </p>
        </div>
        <a
          className='rounded-lg bg-emerald-700 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-800'
          href='/brand/plans.html#choose'
        >
          {d?.status === 'active' ? '管理订阅' : '购买订阅'}
        </a>
      </div>
      {!d ? (
        <div role='status' className='border-t pt-5'>
          <p>
            {query.isError
              ? '暂时无法读取订阅数据，请重试。'
              : '正在读取订阅额度…'}
          </p>
          {query.isError && (
            <Button
              variant='outline'
              className='mt-3'
              onClick={() => void query.refetch()}
            >
              重新加载
            </Button>
          )}
        </div>
      ) : (
        <>
          {query.isError && (
            <p role='alert' className='text-sm text-amber-700'>
              更新失败，以下为上次读取的数据。
              <button
                type='button'
                className='ml-2 underline'
                onClick={() => void query.refetch()}
              >
                重试
              </button>
            </p>
          )}
          <div className='flex flex-wrap items-baseline justify-between gap-2 border-t pt-5'>
            <h3 className='font-semibold'>{subscriptionTitle}</h3>
            <span className='text-muted-foreground text-sm'>
              {subscriptionDetail}
            </span>
          </div>
          <div
            className='rounded-xl border border-emerald-600/30 bg-emerald-50 p-4 sm:p-5 dark:bg-emerald-950/30'
            aria-label={t('Wallet continuation setting')}
          >
            <div className='flex items-center justify-between gap-4'>
              <div className='min-w-0'>
                <label
                  htmlFor='wallet-overflow'
                  className='cursor-pointer text-base font-semibold'
                >
                  {t('Continue with wallet when subscription quota runs out')}
                </label>
                <p
                  id='wallet-overflow-description'
                  className='text-muted-foreground mt-2 text-sm'
                >
                  {t(
                    'When subscription quota runs out, API usage is charged to your wallet balance.'
                  )}
                </p>
              </div>
              <Switch
                id='wallet-overflow'
                aria-describedby='wallet-overflow-description'
                checked={d.account.wallet_overflow}
                disabled={mutate.isPending || preferenceState === 'unknown'}
                onCheckedChange={(v) => {
                  if (!saving.current) mutate.mutate({ wallet_overflow: v })
                }}
                className='!h-8 !w-14 [&_[data-slot=switch-thumb]]:!size-6 [&_[data-slot=switch-thumb][data-checked]]:!translate-x-6 [&_[data-slot=switch-thumb][data-unchecked]]:!translate-x-0.5'
              />
            </div>
            <div className='mt-3 flex flex-wrap items-center gap-3 text-sm'>
              <span className='font-medium'>
                {d.account.wallet_overflow ? t('Enabled') : t('Disabled')}
              </span>
              <span role='status' aria-live='polite'>
                {preferenceState === 'saving' && t('Saving…')}
                {preferenceState === 'verifying' &&
                  t('Verifying saved setting…')}
                {preferenceState === 'effective' && t('Effective')}
              </span>
            </div>
            {!!preferenceError && (
              <p role='alert' className='text-destructive mt-3 text-sm'>
                {preferenceError}
              </p>
            )}
            {preferenceState === 'unknown' && (
              <Button
                variant='outline'
                className='mt-3'
                disabled={query.isFetching}
                onClick={async () => {
                  const result = await query.refetch()
                  if (result.isSuccess) {
                    setPreferenceState('idle')
                    setPreferenceError('')
                  }
                }}
              >
                {t('Reload setting')}
              </Button>
            )}
          </div>
          {d.status === 'active' && (
            <>
              <div className='grid gap-6 sm:grid-cols-2'>
                {displayWindows.map((w) => (
                  <div
                    key={w.key}
                    className='space-y-3'
                    aria-label={w.key === 'five' ? '五小时额度' : '每周额度'}
                  >
                    <div className='flex items-center justify-between'>
                      <h4 className='text-sm font-medium'>
                        {w.key === 'five' ? '五小时额度' : '每周额度'}
                      </h4>
                      <span className='text-muted-foreground text-xs'>
                        {countdown(w.reset_at)}
                      </span>
                    </div>
                    <QuotaGauge remaining={w.remaining} limit={w.limit} />
                    <div className='text-center'>
                      <p className='text-muted-foreground text-xs'>
                        剩余 API 等值额度
                      </p>
                      <p className='mt-1 text-2xl font-semibold tracking-tight text-emerald-700 tabular-nums dark:text-emerald-400'>
                        {amount(w.remaining)}
                      </p>
                    </div>
                    <div className='flex justify-between text-sm tabular-nums'>
                      <span>已用 {amount(w.used)}</span>
                      <span className='text-muted-foreground'>
                        总额 {amount(w.limit)}
                      </span>
                    </div>
                    <p className='text-muted-foreground text-xs'>
                      重置：{date(w.reset_at)}
                    </p>
                  </div>
                ))}
              </div>
              <div className='flex flex-wrap items-center justify-between gap-3 border-t pt-4'>
                <p className='text-muted-foreground text-xs'>
                  {weekExhausted
                    ? t(
                        '本周额度已用完，请等待周额度重置或购买额度重置。'
                      )
                    : '适用窗口同步扣减；已用包含进行中请求的预扣，结算后校正。'}
                </p>
              </div>
              {d.subscription.pending_plan && (
                <p className='text-muted-foreground text-sm'>
                  当前订阅结束后切换为
                  {names[d.subscription.pending_plan] || '已购套餐'}。
                </p>
              )}
            </>
          )}
          {!!d.paused_legacy.length && (
            <details className='border-t pt-4'>
              <summary className='cursor-pointer text-sm font-medium'>
                已保留的旧套餐权益
              </summary>
              <p className='text-muted-foreground mt-2 text-xs'>
                旧套餐已暂停，额度和有效时长均保留；新订阅结束后自动恢复。
              </p>
              {d.paused_legacy.map((p) => (
                <p
                  key={`${p.title}:${p.remaining_quota}:${p.remaining_seconds}`}
                  className='mt-2 text-sm'
                >
                  {p.title} · 剩余有效期{' '}
                  {Math.floor(p.remaining_seconds / 86400)}天{' '}
                  {Math.floor((p.remaining_seconds % 86400) / 3600)}小时 ·
                  剩余额度{' '}
                  {(p.remaining_quota / d.quota_per_unit).toLocaleString(
                    'zh-CN',
                    { maximumFractionDigits: 4 }
                  )}{' '}
                  API美元等值
                </p>
              ))}
            </details>
          )}
          {!!cards.length && (
            <details className='border-t pt-4'>
              <summary className='cursor-pointer text-sm'>
                可用重置卡（{cards.length}）
              </summary>
              {cards.map((c) => (
                <div
                  key={c.id}
                  className='mt-3 flex items-center justify-between text-sm'
                >
                  <span>
                    {c.kind === 'week' ? '周额度卡' : '五小时额度卡'} ·{' '}
                    {date(c.expires)}到期
                  </span>
                  <Button
                    variant='outline'
                    disabled={card.isPending}
                    onClick={() => card.mutate(c.id)}
                  >
                    使用
                  </Button>
                </div>
              ))}
            </details>
          )}
          {d.notices.map((n) => (
            <div
              key={n.key}
              role='status'
              className='border-l-2 border-emerald-600 pl-3 text-sm'
            >
              <strong>{n.title}</strong>
              <p className='text-muted-foreground'>{n.body}</p>
              <button
                type='button'
                className='mt-2 text-emerald-700 underline'
                disabled={notice.isPending}
                onClick={() => notice.mutate(n.key)}
              >
                知道了
              </button>
            </div>
          ))}
          {card.isError && (
            <p role='alert' className='text-sm text-red-600'>
              操作失败，请稍后重试。
            </p>
          )}
        </>
      )}
    </section>
  )
}
