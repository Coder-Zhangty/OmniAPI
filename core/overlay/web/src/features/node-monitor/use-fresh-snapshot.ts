import { useEffect, useState } from 'react'
import type { Snapshot } from './types'

// Expiry continues even when polling fails. Schedule only the next boundary,
// so charts do not render every second during normal successful polling.
export function useFreshSnapshot(source?: Snapshot) {
  const [expiryVersion, setExpiryVersion] = useState(0)
  const now = Date.now() / 1000
  useEffect(() => {
    if (!source) return
    const deadlines = [
      source.timestamp + 30,
      ...source.nodes.flatMap((node) => node.fresh && node.last_seen != null ? [node.last_seen + 30] : []),
    ].filter((deadline) => deadline > Date.now() / 1000)
    if (!deadlines.length) return
    const timer = setTimeout(() => setExpiryVersion((value) => value + 1),
      Math.max(1, Math.ceil((Math.min(...deadlines) - Date.now() / 1000) * 1000) + 1))
    return () => clearTimeout(timer)
  }, [source, expiryVersion])
  const stale = Boolean(source && now - source.timestamp > 30)
  if (!source) return { data: undefined, stale }
  const nodes = source.nodes.map((node) => {
    const fresh = !stale && node.fresh && node.last_seen != null && now - node.last_seen <= 30 && now - node.last_seen >= -30
    return fresh ? node : { ...node, fresh: false, metrics: null, gateway_healthy: null, high_load: false }
  })
  const allFresh = nodes.every((node) => node.fresh)
  return { data: { ...source, nodes, overall_healthy: source.overall_healthy && allFresh,
    model_active: allFresh ? source.model_active : null }, stale }
}
