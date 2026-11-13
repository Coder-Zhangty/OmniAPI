import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
import {
  render,
  screen,
  fireEvent,
  waitFor,
  act,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi, test, expect, beforeEach } from 'vitest'

import { api } from '@/lib/api'

import { OmniAnnouncements } from '../components/omni-announcements'
import { OmniWallet } from '../components/omni-wallet'

vi.mock('@/stores/auth-store', () => ({ useAuthStore: () => 7 }))

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), post: vi.fn() } }))
const summary = {
  status: 'active',
  membership_enabled: true,
  account: { wallet_overflow: true, net_recharge_cents: 20000 },
  level: 'vip',
  bonus_percent: 3,
  server_time: Date.now() / 1000,
  quota_per_unit: 1000,
  five_reset_at: Date.now() / 1000 + 5000,
  week_reset_at: Date.now() / 1000 + 50000,
  cards: [],
  notices: [],
  paused_legacy: [],
  windows: [
    {
      key: 'five',
      limit: 1000,
      used: 500,
      remaining: 500,
      reset_at: Date.now() / 1000 + 5000,
    },
    {
      key: 'week',
      limit: 4000,
      used: 500,
      remaining: 3500,
      reset_at: Date.now() / 1000 + 50000,
    },
  ],
  subscription: {
    plan_key: 'starter',
    start: Date.now() / 1000,
    last_purchase_months: 1,
    months_purchased: 1,
    end: Date.now() / 1000 + 86400,
    fx_micros: 7000000,
    five_limit: 1000,
    five_used: 500,
    week_limit: 4000,
    week_used: 500,
  },
}
function mount(
  component: React.ReactNode,
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
) {
  return render(
    <QueryClientProvider client={client}>{component}</QueryClientProvider>
  )
}
beforeEach(() => {
  vi.mocked(api.get).mockReset()
  vi.mocked(api.post).mockReset()
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: summary },
  })
})

test('weekly exhaustion zeros the five-hour gauge and available amount without changing usage', async () => {
  const exhausted = {
    ...summary,
    windows: summary.windows.map((w) =>
      w.key === 'week' ? { ...w, used: 4010, remaining: 0 } : w
    ),
  }
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: exhausted },
  })
  mount(<OmniWallet />)
  await screen.findByText(
    '本周额度已用完，五小时可用额度为0；请等待每周额度重置。'
  )
  const five = within(screen.getByLabelText('五小时额度'))
  expect(five.getByRole('meter')).toHaveAttribute('aria-valuenow', '0')
  expect(five.getByText('¥0.00')).toBeInTheDocument()
  expect(five.getByText('已用 ¥3.50')).toBeInTheDocument()
  expect(five.getByText('总额 ¥7.00')).toBeInTheDocument()
  expect(five.getByText(/重置：/)).toHaveTextContent(
    new Date(summary.windows[0].reset_at * 1000).toLocaleString('zh-CN', {
      hour12: false,
    })
  )
  expect(exhausted.windows[0].remaining).toBe(500)
  expect(api.post).not.toHaveBeenCalled()
})

test('five-hour availability returns when the server reports renewed weekly quota', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: {
        ...summary,
        windows: summary.windows.map((w) =>
          w.key === 'week' ? { ...w, used: 4000, remaining: 0 } : w
        ),
      },
    },
  })
  mount(<OmniWallet />, client)
  await screen.findByText(
    '本周额度已用完，五小时可用额度为0；请等待每周额度重置。'
  )
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: summary },
  })
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['omni', 'wallet', 7] })
  })
  const five = within(screen.getByLabelText('五小时额度'))
  await waitFor(() =>
    expect(five.getByRole('meter')).toHaveAttribute('aria-valuenow', '50')
  )
  expect(five.getByText('¥3.50')).toBeInTheDocument()
  expect(
    screen.queryByText(
      '本周额度已用完，五小时可用额度为0；请等待每周额度重置。'
    )
  ).not.toBeInTheDocument()
})

test('exhausting only five-hour quota leaves weekly availability visible', async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: {
        ...summary,
        windows: summary.windows.map((w) =>
          w.key === 'five' ? { ...w, used: 1000, remaining: 0 } : w
        ),
      },
    },
  })
  mount(<OmniWallet />)
  await screen.findByText('已用 ¥7.00')
  const week = within(screen.getByLabelText('每周额度'))
  expect(week.getByRole('meter')).toHaveAttribute('aria-valuenow', '88')
  expect(week.getByText('¥24.50')).toBeInTheDocument()
})
test('uses server quota scale and posts wallet fallback preference', async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: summary },
  })
  vi.mocked(api.post).mockResolvedValue({
    data: { success: true, data: { wallet_overflow: false } },
  })
  mount(<OmniWallet />)
  expect(await screen.findByText('¥3.50')).toBeInTheDocument()
  fireEvent.click(
    screen.getByRole('switch', {
      name: 'Continue with wallet when subscription quota runs out',
    })
  )
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/omni/preferences',
      {
        wallet_overflow: false,
      },
      expect.objectContaining({ timeout: 10000 })
    )
  )
  expect(await screen.findByText('Effective')).toBeInTheDocument()
  expect(screen.getByRole('switch')).not.toBeChecked()
})
test('saving disables the switch until the server confirms the authoritative preference', async () => {
  let finish!: (value: unknown) => void
  vi.mocked(api.post).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  mount(<OmniWallet />)
  const toggle = await screen.findByRole('switch')
  fireEvent.click(toggle)
  expect(await screen.findByText('Saving…')).toBeInTheDocument()
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  expect(toggle).toBeChecked()
  await waitFor(() => expect(api.post).toHaveBeenCalled())
  await act(async () =>
    finish({ data: { success: true, data: { wallet_overflow: false } } })
  )
  expect(await screen.findByText('Effective')).toBeInTheDocument()
  expect(toggle).not.toBeChecked()
  expect(toggle).not.toHaveAttribute('aria-disabled', 'true')
})
test('explicit rejection retains the existing preference and displays the reason', async () => {
  vi.mocked(api.post).mockResolvedValue({
    data: { success: false, message: 'Preference rejected' },
  })
  mount(<OmniWallet />)
  fireEvent.click(await screen.findByRole('switch'))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Preference rejected'
  )
  expect(screen.getByRole('switch')).toBeChecked()
  expect(screen.queryByText('Effective')).not.toBeInTheDocument()
})
test('a timeout verifies the saved preference before displaying effective', async () => {
  let finish!: (value: unknown) => void
  vi.mocked(api.post).mockRejectedValue({
    isAxiosError: true,
    code: 'ECONNABORTED',
  })
  mount(<OmniWallet />)
  const toggle = await screen.findByRole('switch')
  vi.mocked(api.get).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  fireEvent.click(toggle)
  expect(
    await screen.findByText('Verifying saved setting…')
  ).toBeInTheDocument()
  expect(screen.queryByText('Effective')).not.toBeInTheDocument()
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await act(async () =>
    finish({
      data: {
        success: true,
        data: { ...summary, account: { wallet_overflow: false } },
      },
    })
  )
  expect(await screen.findByText('Effective')).toBeInTheDocument()
  expect(toggle).not.toBeChecked()
})
test('a timeout with the old server preference does not report success', async () => {
  vi.mocked(api.post).mockRejectedValue({
    isAxiosError: true,
    code: 'ECONNABORTED',
  })
  mount(<OmniWallet />)
  fireEvent.click(await screen.findByRole('switch'))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Current setting verified, but this change is not confirmed. Reload before trying again.'
  )
  expect(screen.getByRole('switch')).toBeChecked()
  expect(screen.queryByText('Effective')).not.toBeInTheDocument()
  expect(screen.getByRole('switch')).toHaveAttribute('aria-disabled', 'true')
})
test('late pre-save query responses cannot overwrite the confirmed preference', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  let finish!: (value: unknown) => void
  vi.mocked(api.post).mockResolvedValue({
    data: { success: true, data: { wallet_overflow: false } },
  })
  mount(<OmniWallet />, client)
  const toggle = await screen.findByRole('switch')
  vi.mocked(api.get).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  void client.invalidateQueries({ queryKey: ['omni', 'wallet', 7] })
  await waitFor(() => expect(api.get).toHaveBeenCalledTimes(2))
  const config = vi.mocked(api.get).mock.calls[1][1]
  fireEvent.click(toggle)
  expect(await screen.findByText('Effective')).toBeInTheDocument()
  expect(config?.signal?.aborted).toBe(true)
  await act(async () => finish({ data: { success: true, data: summary } }))
  expect(toggle).not.toBeChecked()
})
test('wallet fallback supports keyboard activation and has a visible billing description', async () => {
  vi.mocked(api.post).mockResolvedValue({
    data: { success: true, data: { wallet_overflow: false } },
  })
  mount(<OmniWallet />)
  const toggle = await screen.findByRole('switch')
  expect(toggle).toHaveAccessibleDescription(
    'When subscription quota runs out, API usage is charged to your wallet balance.'
  )
  toggle.focus()
  await userEvent.keyboard(' ')
  expect(await screen.findByText('Effective')).toBeInTheDocument()
  expect(toggle).not.toBeChecked()
})
test('announcements acknowledge through user-scoped server records', async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        { key: 'announcement:1:7', title: '新订阅体系', body: '额度说明' },
      ],
    },
  })
  vi.mocked(api.post).mockResolvedValue({ data: { success: true } })
  mount(<OmniAnnouncements />)
  fireEvent.click(await screen.findByText('知道了'))
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith('/api/omni/notices/read', {
      key: 'announcement:1:7',
    })
  )
})
