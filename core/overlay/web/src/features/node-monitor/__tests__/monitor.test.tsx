import { act, render, renderHook, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { NodeCard } from '../nodes'
import type { MonitorNode, Snapshot } from '../types'
import { bytes } from '../format'
import { RequestHistory } from '../history'
import { useFreshSnapshot } from '../use-fresh-snapshot'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'en' } }) }))
describe('Unavailable node metrics', () => {
  it('expires individual cached nodes without another successful API response', () => {
    vi.useFakeTimers()
    const now = Date.now() / 1000
    const source: Snapshot = { timestamp: now - 10, nodes: [{ id: 'primary', ip: '103.121.93.231', fresh: true, last_seen: now - 25, gateway_healthy: true, metrics: { timestamp: now - 25, cpu_cores: 4, cpu_percent: 50, memory_total: 1024, memory_used: 512, memory_percent: 50, load: [1, 1, 1], load_per_core: 0.25, network_rx: 1, network_tx: 1, disks: [], model_active: 2, active: 2, observer_dropped: 0 } }], health: { durable: true }, routing: { configured: { revision: 1, primary: 100, relay: 0, relay_enabled: true, draining: null }, effective: { primary: 100, relay: 0 }, actual: null }, model_active: 2, overall_healthy: true, operations: [], started_at: now }
    const { result, unmount } = renderHook(() => useFreshSnapshot(source))
    expect(result.current.data?.nodes[0].fresh).toBe(true)
    act(() => vi.advanceTimersByTime(5002))
    expect(result.current.data?.nodes[0].fresh).toBe(false)
    expect(result.current.data?.nodes[0].metrics).toBeNull()
    expect(result.current.data?.model_active).toBeNull()
    expect(result.current.data?.overall_healthy).toBe(false)
    act(() => vi.advanceTimersByTime(20000))
    expect(result.current.stale).toBe(true)
    unmount();vi.useRealTimers()
  })
  it('does not present disconnected metrics as zero or healthy', () => {
    const node: MonitorNode = { id: 'relay', ip: '160.202.46.124', fresh: false, metrics: null, last_seen: null, gateway_healthy: null }
    render(<NodeCard node={node} expanded onSelect={() => undefined} />)
    expect(screen.getByText('Collector disconnected')).toBeInTheDocument()
    expect(screen.getByText('Gateway · Unknown')).toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    expect(bytes(null, 'en')).toBe('—')
  })
  it('renders a gateway request with no channel chain without crashing', () => {
    render(<RequestHistory page={1} onPage={() => undefined} data={{ page: 1, page_size: 25, total: 1, items: [{ timestamp: Date.now(), event_id: 'test-null-chain', edge_id: 'test-null-chain', request_id: 'request-1', node: 'primary', category: 'manage', model: '', outcome: 'rejected', duration_ms: 12, first_ms: null, attempts: 0, chain: null }] }} />)
    expect(screen.getByText('request-1')).toBeInTheDocument()
    expect(screen.getByText('rejected')).toBeInTheDocument()
  })
})
