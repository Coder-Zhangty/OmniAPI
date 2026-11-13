/** Public display-index contract. Admin and billing responses are separate. */
export type RankingPeriod = 'today' | 'week' | 'month' | 'year' | 'all'
export interface DisplayIndexMetadata {
  label: '展示指数'
  scale: 200
  unit: 'INDEX'
  simulated_start: string
  simulated_end_exclusive: string
}
export interface SnapshotManifest {
  schema: 1
  version: string
  generated_at: string
  periods: Record<RankingPeriod, {path: string; sha256: string}>
}
export interface RankingsDisplaySnapshot {
  period: RankingPeriod
  /** Sum over every model, including chart Others and models beyond top 20. */
  total_tokens: number
  all_models_count: number
  display: DisplayIndexMetadata
  window: {start: string; end: string; timezone: 'Asia/Shanghai'; comparison_available: boolean}
  models: Array<{rank: number; previous_rank?: number; model_name: string; vendor: string; vendor_icon?: string; category: 'all'; total_tokens: number; share: number; growth_pct: number | null}>
  vendors: Array<{rank: number; vendor: string; vendor_icon?: string; total_tokens: number; share: number; growth_pct: number | null; models_count: number; top_model: string}>
  top_movers: RankingMover[]
  top_droppers: RankingMover[]
  models_history: {points: Array<{ts: string; label: string; model: string; vendor: string; tokens: number}>; models: Array<{name: string; vendor: string; total: number}>; buckets: number}
  vendor_share_history: {points: Array<{ts: string; label: string; vendor: string; tokens: number; share: number}>; vendors: Array<{name: string; total: number; share: number}>; buckets: number}
}
export interface RankingMover {
  model_name: string
  vendor: string
  vendor_icon?: string
  rank_delta: number
  current_rank: number
  growth_pct: number
}
