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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { UserDiscountSection } from '../user-discount-section'

const record = (version = 'version-1', conflict = false) => ({
  success: true,
  data: { version, user_id: 16, legacy_conflict: conflict, rule: {
    enabled: false, target_margin: 0.4, basis: 'procurement_margin',
    funding_scope: 'wallet', model_scope: 'text', public_label: '价格优惠',
  } },
})

function viewer(role: number) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'operator', role })
}

afterEach(() => { vi.restoreAllMocks(); useAuthStore.getState().auth.reset() })

test.each([1, 10])('role %i never loads or exposes discount controls', (role) => {
  viewer(role)
  const get = vi.spyOn(api, 'get')
  render(<UserDiscountSection userId={16} open />)
  expect(screen.queryByText('Price discount')).not.toBeInTheDocument()
  expect(get).not.toHaveBeenCalled()
})

test('root saves only version, enable and decimal target margin to dedicated API', async () => {
  viewer(100)
  vi.spyOn(api, 'get').mockResolvedValue({ data: record() })
  const put = vi.spyOn(api, 'put').mockResolvedValue({ data: record('version-2') })
  render(<UserDiscountSection userId={16} open />)
  const input = await screen.findByLabelText('Target gross margin (%)')
  await waitFor(() => expect(input).toBeEnabled())
  await userEvent.clear(input); await userEvent.type(input, '50')
  await userEvent.click(screen.getByRole('checkbox', { name: 'Enable price discount' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save price discount' }))
  await waitFor(() => expect(put).toHaveBeenCalledWith('/api/omni/admin/users/16/discount', {
    expected_version: 'version-1', enabled: true, target_margin: 0.5,
  }))
  expect(put).toHaveBeenCalledTimes(1)
})

test('invalid margin cannot save', async () => {
  viewer(100)
  vi.spyOn(api, 'get').mockResolvedValue({ data: record() })
  const put = vi.spyOn(api, 'put')
  render(<UserDiscountSection userId={16} open />)
  const input = await screen.findByLabelText('Target gross margin (%)')
  await waitFor(() => expect(input).toBeEnabled())
  await userEvent.clear(input); await userEvent.type(input, '91')
  expect(screen.getByRole('button', { name: 'Save price discount' })).toBeDisabled()
  expect(put).not.toHaveBeenCalled()
})

test('409 blocks saving until an explicit reload supplies the new version', async () => {
  viewer(100)
  const get = vi.spyOn(api, 'get').mockResolvedValueOnce({ data: record() }).mockResolvedValueOnce({ data: record('version-2') })
  const put = vi.spyOn(api, 'put').mockRejectedValueOnce({ response: { status: 409 } })
  render(<UserDiscountSection userId={16} open />)
  const save = await screen.findByRole('button', { name: 'Save price discount' })
  await waitFor(() => expect(save).toBeEnabled())
  await userEvent.click(save)
  expect(await screen.findByText('Price discount changed. Reload before editing again.')).toBeVisible()
  expect(save).toBeDisabled(); expect(put).toHaveBeenCalledTimes(1)
  await userEvent.click(screen.getByRole('button', { name: 'Reload' }))
  await waitFor(() => expect(save).toBeEnabled())
  expect(get).toHaveBeenCalledTimes(2)
})

test('legacy conflict prevents enabling and discloses the conflict', async () => {
  viewer(100)
  vi.spyOn(api, 'get').mockResolvedValue({ data: record('version-1', true) })
  render(<UserDiscountSection userId={16} open />)
  expect(await screen.findByText('An existing billing rule conflicts. Resolve it before enabling this discount.')).toBeVisible()
  expect(screen.getByRole('checkbox', { name: 'Enable price discount' })).toHaveAttribute('aria-disabled', 'true')
})

test('failed loading leaves save unavailable and offers explicit reload', async () => {
  viewer(100)
  vi.spyOn(api, 'get').mockRejectedValue(new Error('unavailable'))
  render(<UserDiscountSection userId={16} open />)
  expect(await screen.findByText('Failed to load price discount')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save price discount' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Reload' })).toBeEnabled()
})
