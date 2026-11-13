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
import { render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import { DetailsDialog } from '../dialogs/details-dialog'

vi.mock('@/features/pricing/hooks/use-pricing-data', () => ({
  usePricingData: () => ({ data: null, models: [], pricing: [] }),
}))

test.each([false, true])('settled fee in details shows only the public discount label for admin=%s', (isAdmin) => {
  const log = usageLogSchema.parse({
    id: 1, user_id: 16, created_at: 1, type: 2, content: '', quota: 5000,
    other: JSON.stringify({ billing_mode: 'tiered_expr', price_discount_applied: true }),
  })
  render(<DetailsDialog log={log} isAdmin={isAdmin} isRoot={false} open onOpenChange={() => undefined} />)
  expect(screen.getByText('$0.01（价格优惠）')).toBeVisible()
})

test('details do not mark old logs or derive a discount from user id', () => {
  const log = usageLogSchema.parse({ id: 1, user_id: 16, created_at: 1, type: 2, content: '', quota: 5000, other: '{}' })
  render(<DetailsDialog log={log} isAdmin={false} isRoot={false} open onOpenChange={() => undefined} />)
  expect(screen.queryByText(/价格优惠/)).not.toBeInTheDocument()
})
