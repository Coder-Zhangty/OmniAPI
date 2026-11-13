import { useQuery } from '@tanstack/react-query'
import { KeyRound, UsersRound } from 'lucide-react'
import { api } from '@/lib/api'
import type { ApiKey } from '../types'

export function sharedFallbackLabel(mode?: string | null) {
  return ({
    off: '共享不足即停止',
    subscription_only: '共享不足 → 个人套餐',
    wallet_only: '共享不足 → 个人钱包',
    subscription_then_wallet: '共享不足 → 个人套餐 → 钱包',
  } as Record<string, string>)[mode || 'off'] || '续用策略未知，请检查设置'
}

export function ApiKeyFunding({ apiKey, detail = false }: { apiKey: Partial<ApiKey>; detail?: boolean }) {
  const pool = apiKey.omni_pool_id || ''
  const { data: pools } = useQuery({
    queryKey: ['shared-pool-labels', apiKey.user_id || 0],
    enabled: Boolean(pool),
    staleTime: 30_000,
    retry: false,
    queryFn: async () => {
      const response = await api.get('/api/omni/shared', { skipErrorHandler: true })
      if (!response.data?.success) throw Error('无法读取共享池名称')
      return Array.isArray(response.data.data) ? response.data.data : []
    },
  })
  const name = pools?.find((item: { pool_id: string }) => item.pool_id === pool)?.name
  const Icon = pool ? UsersRound : KeyRound
  return (
    <div data-key-funding={pool ? 'shared' : 'personal'} className='mt-1.5 min-w-0 space-y-1 text-xs leading-5'>
      <span className={pool ? 'inline-flex items-center gap-1.5 rounded-md bg-blue-50 px-2 py-0.5 font-semibold text-blue-700 dark:bg-blue-950 dark:text-blue-200' : 'inline-flex items-center gap-1.5 rounded-md bg-muted px-2 py-0.5 font-medium text-muted-foreground'}>
        <Icon className='size-3.5' aria-hidden />
        {pool ? '共享池 Key' : '个人 Key'}
        {apiKey.omni_shared_revoked ? ' · 已失效' : ''}
      </span>
      {pool ? <>
        <a className='block break-words font-medium text-foreground underline-offset-4 hover:underline focus-visible:outline focus-visible:outline-2' href='/shared' title={pool}>
          {name || `共享池 · ${pool.slice(0, 8)}…`}
        </a>
        <span className='block break-words text-muted-foreground'>{sharedFallbackLabel(apiKey.omni_fallback)}</span>
        {detail && <>
          <p className='text-muted-foreground'>优先扣此共享池。额度上限只限制本 Key，不代表池余额；个人续用消费不计入共享图。</p>
          <a href='/shared#model-limits' className='block font-medium underline underline-offset-4'>查看共享模型与请求限额 →</a>
        </>}
      </> : <span className='block text-muted-foreground'>使用个人套餐／钱包</span>}
      {detail && !pool && <a href='/shared' className='block font-medium text-blue-700 underline underline-offset-4 dark:text-blue-200'>需要共用额度？去共享池创建共享 Key →</a>}
    </div>
  )
}
