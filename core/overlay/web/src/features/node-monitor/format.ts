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
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

export function nodeLabel(node: string) {
  if (node === 'primary') return 'Primary node'
  if (node === 'relay') return 'Relay node'
  return 'Unknown node'
}
export function filterOptions(key: string) {
  if (key === 'node') return ['primary', 'relay', 'unknown']
  if (key === 'category') {
    return ['model', 'login', 'management', 'recharge', 'support', 'sync']
  }
  return ['success', 'failure', 'rejected', 'cancelled', 'edge_rejected']
}

export function number(value: number | null | undefined, language: string) {
  return value == null ? '—' : formatNumber(value, toIntlLocale(language))
}
export function bytes(value: number | null | undefined, language: string) {
  if (value == null) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const exponent = Math.min(
    4,
    Math.max(0, Math.floor(Math.log2(Math.max(1, value)) / 10))
  )
  return `${number(value / 1024 ** exponent, language)} ${units[exponent]}`
}
export function date(value: number | null, language: string) {
  return value == null
    ? '—'
    : new Intl.DateTimeFormat(toIntlLocale(language), {
        dateStyle: 'short',
        timeStyle: 'medium',
      }).format(value * 1000)
}
