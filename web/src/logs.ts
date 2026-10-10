import type { AdminLog } from './types'

// LogRow stores the latest state and full event timeline of a request
export interface LogRow {
  key: string
  entry: AdminLog
  events: AdminLog[]
}

// entryKeys assigns row keys for non-request logs that do not change with array truncation
const entryKeys = new WeakMap<AdminLog, string>()
let nextEntryKey = 0

// entryKey returns a stable row key for a log entry
function entryKey(entry: AdminLog): string {
  let key = entryKeys.get(entry)
  if (key === undefined) {
    key = `log:${nextEntryKey++}`
    entryKeys.set(entry, key)
  }
  return key
}

// numberFormats caches numeric formatters by locale and fraction digits
const numberFormats = new Map<string, Intl.NumberFormat>()

// formatLogNumber formats log metric numbers according to locale
export function formatLogNumber(value: number, locale: string, digits = 0): string {
  const key = `${locale}:${digits}`
  let format = numberFormats.get(key)
  if (format === undefined) {
    format = new Intl.NumberFormat(locale, { maximumFractionDigits: digits })
    numberFormats.set(key, format)
  }
  return format.format(value)
}

// groupLogs merges lifecycles by request ID while preserving original order of service events
export function groupLogs(logs: AdminLog[]): LogRow[] {
  const rows: LogRow[] = []
  const requests = new Map<string, LogRow>()
  for (const entry of logs) {
    const id = entry.request?.id
    const row = id ? requests.get(id) : undefined
    if (row) {
      row.events.push(entry)
      row.entry = { ...entry, request: { ...row.entry.request!, ...entry.request! } }
    } else {
      const next = { key: id || entryKey(entry), entry, events: [entry] }
      rows.push(next)
      if (id) requests.set(id, next)
    }
  }
  return rows
}
