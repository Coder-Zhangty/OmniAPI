import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, test, vi, expect } from 'vitest'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

import { channelSchema } from '../../types'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { ChannelTestDialog } from '../dialogs/channel-test-dialog'

const row = channelSchema.parse({
  id: 7,
  type: 1,
  key: '',
  status: 2,
  name: 'Diagnostic',
  models: 'test-model',
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
})
function Harness() {
  const state = useChannels()
  return (
    <>
      <Button onClick={() => state.setCurrentRow(row)}>Open diagnostic</Button>
      <ChannelTestDialog open onOpenChange={() => {}} />
    </>
  )
}
function setup() {
  const query = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={query}>
      <ChannelsProvider>
        <Harness />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  return userEvent.setup()
}
describe('fixed-channel diagnostics', () => {
  test('missing test parameters show not executed and cannot be deleted as failed', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: false,
        diagnostic_state: 'not_executed',
        error_code: 'diagnostic_not_executed',
        message: '未执行：参数未确认',
        time: 0,
      },
    })
    const user = setup()
    await user.click(screen.getByRole('button', { name: 'Open diagnostic' }))
    await user.click(
      await screen.findByRole('button', { name: /Test all 1 models/ })
    )
    await screen.findByText('未执行')
    expect(
      screen.queryByRole('button', { name: /Delete failed models/ })
    ).not.toBeInTheDocument()
  })
  test('valid unpriced response is passed and public price remains unavailable', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        diagnostic_state: 'passed',
        canonical_model: 'standard-model',
        price_ready: false,
        time: 0.2,
      },
    })
    const user = setup()
    await user.click(screen.getByRole('button', { name: 'Open diagnostic' }))
    await user.click(
      await screen.findByRole('button', { name: /Test all 1 models/ })
    )
    await waitFor(() =>
      expect(screen.getByText(/standard-model/)).toBeInTheDocument()
    )
    expect(screen.getByText(/售价尚未开放/)).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Delete failed models/ })
    ).not.toBeInTheDocument()
  })
})
