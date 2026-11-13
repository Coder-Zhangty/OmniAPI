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
export type NodeID = 'primary' | 'relay'
export type Outcome =
  | 'success'
  | 'failure'
  | 'rejected'
  | 'cancelled'
  | 'edge_rejected'
export interface Disk {
  device: string
  mount: string
  total: number
  used: number
  free: number
  percent: number
  role?: 'business' | 'maintenance' | 'display'
  business_paths?: string[]
  readonly?: boolean
  alert_level?: 'normal' | 'warning' | 'critical' | 'maintenance'
}
export interface Metrics {
  timestamp: number
  cpu_cores: number
  cpu_percent: number | null
  memory_total: number
  memory_used: number
  memory_percent: number | null
  load: number[]
  load_per_core: number | null
  network_rx: number | null
  network_tx: number | null
  disks: Disk[]
  model_active: number | null
  active: number | null
  observer_dropped: number | null
}
export interface MonitorNode {
  id: NodeID
  ip: string
  fresh: boolean
  metrics: Metrics | null
  last_seen: number | null
  gateway_healthy: boolean | null
  high_load?: boolean
}
export interface Routing {
  revision: number
  primary: number
  relay: number
  relay_enabled: boolean
  draining: NodeID | null
}
export interface Job {
  id: string
  action: string
  node?: NodeID
  state: string
  message: string
  active_requests?: number
  remaining_seconds?: number
  created_at?: number
}
export interface Snapshot {
  timestamp: number
  nodes: MonitorNode[]
  health: Record<string, boolean | number | null>
  routing: {
    configured: Routing
    effective: Record<NodeID, number> | null
    actual: Record<NodeID, number> | null
  }
  model_active: number | null
  started_at: number | null
  operations: Job[]
  overall_healthy?: boolean
  recent_exceptions?: MonitorEvent[]
  disk_issues?: { node: NodeID; severity: 'warning' | 'critical' | 'maintenance'; detail: string }[]
}
export interface RequestRow {
  event_id: string
  edge_id: string
  timestamp: number
  node: string
  category: string
  model: string
  outcome: Outcome
  duration_ms: number
  first_ms: number | null
  attempts: number
  chain: string[] | null
  request_id: string
}
export interface RequestPoint extends Record<Outcome, number> {
  timestamp: number
  node: string
  count: number
  attempts: number
  rpm: number
  p50_ms: number | null
  p95_ms: number | null
  first_p50_ms: number | null
  first_p95_ms: number | null
}
export interface ResourcePoint {
  timestamp: number
  node: string
  cpu_percent?: number
  memory_percent?: number
  load_per_core?: number
  network_rx?: number
  network_tx?: number
  peak: Record<string, number>
}
export interface Summary extends Record<Outcome, number> {
  total: number
  node_counts: Record<string, number>
  success_rate: number | null
  p50_ms: number | null
  p95_ms: number | null
  first_p50_ms: number | null
  first_p95_ms: number | null
}
export interface Series {
  requests: RequestPoint[]
  resources: ResourcePoint[]
  summary: Summary
  step: number
}
export interface MonitorEvent {
  id: string
  timestamp: number
  kind: string
  node: string
  title?: string
  detail?: string
  status?: string
  phase?: string
  error?: string
  action?: string
  message?: string
}
export interface Page<T> {
  items: T[]
  total: number
  page?: number
  page_size?: number
}
export type Params = Record<string, string | number>
export type Operation =
  | { action: 'set_weights'; primary: number; relay: number }
  | { action: 'set_relay_enabled'; enabled: boolean }
  | { action: 'restart_gateway'; node: NodeID }
