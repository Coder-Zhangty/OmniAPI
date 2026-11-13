import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, test, vi } from 'vitest'

import { UserSubscriptionsDialog } from '@/features/subscriptions/components/dialogs/user-subscriptions-dialog'

import { getUserInfo } from '../../api'
import type { UserInfo } from '../../types'
import { UserInfoDialog } from '../dialogs/user-info-dialog'

vi.mock('../../api', () => ({ getUserInfo: vi.fn() }))
vi.mock('@/features/subscriptions/api', () => ({
  getAdminPlans: vi.fn(async () => ({ success: true, data: [] })),
  getUserSubscriptions: vi.fn(async () => ({ success: true, data: [] })),
}))
const info: UserInfo = {
  id: 16, username: 'billing-fixture', quota: 500000, used_quota: 1000000,
  request_count: 283,
  billing: { server_time: 1000000000, status: 'active', plan_key: 'standard', start: 1, end: 2000000000,
    wallet_overflow: true, quota_per_unit: 500000, fx_micros: 7000000,
    windows: [{ key: 'five', limit: 500000, used: 300000, remaining: 200000, paused: false, reset_at: 2000000000 }] },
}

function mount(component: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{component}</QueryClientProvider>)
}
beforeEach(() => {
  vi.mocked(getUserInfo).mockReset()
  vi.mocked(getUserInfo).mockResolvedValue({ success: true, data: info })
})
test('shows wallet and new subscription allowance separately and refreshes the balance', async () => {
  mount(<UserInfoDialog userId={16} open onOpenChange={() => {}} />)
  expect(await screen.findByText('Wallet balance')).toBeVisible()
  expect(screen.getByText(/Current dual-window subscription/)).toBeVisible()
  expect(screen.getByText('Five-hour allowance')).toBeVisible()
  vi.mocked(getUserInfo).mockResolvedValue({ success: true, data: { ...info, quota: 250000 } })
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() => expect(getUserInfo).toHaveBeenCalledTimes(2))
  expect(await screen.findByText('$0.5')).toBeVisible()
})
test('empty legacy records do not conceal a current dual-window subscription', async () => {
  mount(<UserSubscriptionsDialog user={{ id: 16, username: 'billing-fixture' }} open onOpenChange={() => {}} />)
  expect(await screen.findByText(/Current dual-window subscription/)).toBeVisible()
  expect(await screen.findByText('No legacy subscription records')).toBeVisible()
})
test('failed refresh visibly warns while retaining the last confirmed balance', async () => {
  mount(<UserInfoDialog userId={16} open onOpenChange={() => {}} />)
  await screen.findByText('Wallet balance')
  vi.mocked(getUserInfo).mockRejectedValue(new Error('offline'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not refresh user information')
  expect(screen.getByText('Wallet balance')).toBeVisible()
})
