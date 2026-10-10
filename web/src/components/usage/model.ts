import { onUnmounted, reactive, watch } from 'vue'
import type { UsageDimension, UsageFilters, UsageStats } from '@/types'

// relativePresets and calendarPresets are shortcut time ranges for relative time and calendar periods
export const relativePresets = ['1h', '6h', '24h', '7d', '30d', '90d'] as const
export const calendarPresets = ['today', 'yesterday', 'week', 'month', 'lastMonth'] as const
export type RangePreset =
  (typeof relativePresets)[number] | (typeof calendarPresets)[number] | 'custom'

export const dimensions: UsageDimension[] = ['model', 'account', 'channel', 'protocol', 'state']

// maxSpan matches server validation limit
export const maxSpan = 93 * 86_400_000
export const retention = 90 * 86_400_000

const relativeSpans: Record<(typeof relativePresets)[number], number> = {
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 86_400_000,
  '7d': 7 * 86_400_000,
  '30d': 30 * 86_400_000,
  '90d': 90 * 86_400_000,
}

// UsageViewState is the view state persisted to address bar for the usage dashboard
export interface UsageViewState {
  range: RangePreset
  from: string
  to: string
  stack: UsageDimension
  filters: UsageFilters
  status: string
  search: string
}

const defaults: UsageViewState = {
  range: '24h',
  from: '',
  to: '',
  stack: 'model',
  filters: {},
  status: '',
  search: '',
}

// readState reads view state from address bar query parameters, falling back to defaults for invalid values
function readState(): UsageViewState {
  const query = new URLSearchParams(window.location.search)
  const range = query.get('range') as RangePreset | null
  const presets: readonly string[] = [...relativePresets, ...calendarPresets, 'custom']
  const stack = query.get('stack') as UsageDimension | null
  const filters: UsageFilters = {}
  for (const dimension of dimensions) {
    const values = (query.get(dimension) ?? '').split(',').filter((value) => value !== '')
    if (values.length > 0) filters[dimension] = values
  }
  return {
    range: range !== null && presets.includes(range) ? range : defaults.range,
    from: query.get('from') ?? '',
    to: query.get('to') ?? '',
    stack: stack !== null && dimensions.includes(stack) ? stack : defaults.stack,
    filters,
    status: /^\d{3}$/.test(query.get('status') ?? '') ? (query.get('status') ?? '') : '',
    search: query.get('q') ?? '',
  }
}

// writeState writes non-default view state via replaceState, preserving tab and other parameters
function writeState(state: UsageViewState): void {
  const url = new URL(window.location.href)
  const set = (name: string, value: string, fallback = ''): void => {
    if (value === fallback) url.searchParams.delete(name)
    else url.searchParams.set(name, value)
  }
  set('range', state.range, defaults.range)
  set('from', state.range === 'custom' ? state.from : '')
  set('to', state.range === 'custom' ? state.to : '')
  set('stack', state.stack, defaults.stack)
  for (const dimension of dimensions) set(dimension, (state.filters[dimension] ?? []).join(','))
  set('status', state.status)
  set('q', state.search)
  if (url.href !== window.location.href) window.history.replaceState(null, '', url)
}

// useUsageState returns view state bidirectionally synced with address bar query parameters
export function useUsageState(): UsageViewState {
  const state = reactive(readState())
  watch(
    () => JSON.stringify(state),
    () => writeState(state),
  )
  const sync = (): void => {
    Object.assign(state, readState())
  }
  window.addEventListener('popstate', sync)
  onUnmounted(() => window.removeEventListener('popstate', sync))
  return state
}

// localInput formats Date into local datetime string suitable for datetime-local inputs
export function localInput(date: Date): string {
  const pad = (value: number): string => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// resolveRange resolves view state into concrete start and end timestamps, returning null if custom range is invalid
export function resolveRange(
  state: Pick<UsageViewState, 'range' | 'from' | 'to'>,
  now: Date,
): { from: Date; to: Date } | null {
  const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  switch (state.range) {
    case 'today':
      return { from: midnight, to: now }
    case 'yesterday':
      return {
        from: new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1),
        to: midnight,
      }
    case 'week': {
      const weekday = (now.getDay() + 6) % 7
      return {
        from: new Date(now.getFullYear(), now.getMonth(), now.getDate() - weekday),
        to: now,
      }
    }
    case 'month':
      return { from: new Date(now.getFullYear(), now.getMonth(), 1), to: now }
    case 'lastMonth':
      return {
        from: new Date(now.getFullYear(), now.getMonth() - 1, 1),
        to: new Date(now.getFullYear(), now.getMonth(), 1),
      }
    case 'custom': {
      const from = new Date(state.from)
      const to = new Date(state.to)
      return validRange(from, to) === '' ? { from, to } : null
    }
    default:
      return { from: new Date(now.getTime() - relativeSpans[state.range]), to: now }
  }
}

// validRange validates custom date range, returning error translation key or empty string
export function validRange(from: Date, to: Date): '' | 'usage.rangeInvalid' | 'usage.rangeTooLong' {
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()) || from >= to)
    return 'usage.rangeInvalid'
  if (to.getTime() - from.getTime() > maxSpan) return 'usage.rangeTooLong'
  return ''
}

// liveRange determines whether end of range tracks current time to enable auto-refresh
export function liveRange(range: RangePreset): boolean {
  return range !== 'yesterday' && range !== 'lastMonth' && range !== 'custom'
}

// successRate returns success ratio excluding client-cancelled requests
export function successRate(stats: UsageStats): number | null {
  const counted = stats.requests - stats.canceled
  return counted > 0 ? stats.succeeded / counted : null
}

// palette contains colors for categorical series, hashed for visual consistency across charts
export const palette = [
  '#60a5fa',
  '#34d399',
  '#f59e0b',
  '#a78bfa',
  '#f472b6',
  '#22d3ee',
  '#fb923c',
  '#a3e635',
  '#e879f9',
  '#2dd4bf',
]
export const otherColor = '#6b7280'

// assignColors assigns colors for a set of values, resolving hash collisions to unused colors
export function assignColors(keys: string[]): Map<string, string> {
  const result = new Map<string, string>()
  const used = new Set<number>()
  for (const key of keys) {
    let hash = 0
    for (const char of key) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
    let index = hash % palette.length
    for (let probe = 0; probe < palette.length && used.has(index); probe++)
      index = (index + 1) % palette.length
    used.add(index)
    result.set(key, palette[index] ?? otherColor)
  }
  return result
}

// protocolLabels maps public protocol codes to display labels
export const protocolLabels: Record<string, string> = {
  'openai-chat': 'OpenAI Chat',
  'openai-responses': 'OpenAI Responses',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  interactions: 'Interactions',
  images: 'Images',
  audio: 'Audio',
  videos: 'Videos',
  files: 'Files',
  other: 'Other',
}

// latencyTone returns color tone based on latency thresholds
export function latencyTone(milliseconds: number, kind: 'duration' | 'first'): string {
  const [warn, slow] = kind === 'first' ? [10_000, 30_000] : [60_000, 180_000]
  if (milliseconds >= slow) return 'bg-red-500'
  if (milliseconds >= warn) return 'bg-yellow-500'
  return 'bg-green-500'
}

// rateTone returns text tone based on success rate thresholds
export function rateTone(rate: number | null): string {
  if (rate === null) return 'text-gray-500'
  if (rate >= 0.95) return 'text-green-400'
  if (rate >= 0.8) return 'text-yellow-400'
  return 'text-red-400'
}

// readPreference reads UI preferences from localStorage, falling back to defaults
export function readPreference<T>(key: string, fallback: T, valid: (value: unknown) => boolean): T {
  try {
    const raw = window.localStorage.getItem(key)
    if (raw === null) return fallback
    const value: unknown = JSON.parse(raw)
    return valid(value) ? (value as T) : fallback
  } catch {
    return fallback
  }
}

// writePreference saves UI preferences to localStorage, ignoring when unavailable
export function writePreference(key: string, value: unknown): void {
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
    return
  }
}
