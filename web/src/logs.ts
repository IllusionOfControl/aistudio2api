import type { AdminLog } from './types'

// LogRow stores the latest state and full event timeline of a request or stage
export interface LogRow {
  key: string
  entry: AdminLog
  events: AdminLog[]
  stage?: LogStage
}

// LogStageState indicates the progress state of a staged operation
export type LogStageState = 'running' | 'completed' | 'failed' | 'cancelled'

// LogStage stores merged progress across staged log entries from the same source
export interface LogStage {
  name: string
  step: number
  steps: number
  detail: string
  state: LogStageState
}

// StageRow is a log row with stage progress attached
type StageRow = LogRow & { stage: LogStage }

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

// stagePattern matches stage step messages in "Stage Name | i/N | details" format
const stagePattern = /^(.+?) \| (\d+)\/(\d+)(?: \| ([\s\S]*))?$/

// stageReadyTitles maps ready log message titles to their corresponding stage names
const stageReadyTitles: Record<string, string> = {
  'Protocol runtime ready': 'Runtime assembly',
  'Generation service ready': 'Generation service startup',
  'WAA worker ready': 'WAA worker startup',
  'WAA Worker ready': 'WAA worker startup',
}

// stageEndSuffixes maps message title suffixes to their corresponding terminal states
const stageEndSuffixes: [string, LogStageState][] = [
  [' completed', 'completed'],
  [' failed', 'failed'],
  [' cancelled', 'cancelled'],
]

// stageEnd returns the stage name and terminal state for an ending message title
function stageEnd(title: string): [string, LogStageState] | undefined {
  const ready = stageReadyTitles[title]
  if (ready !== undefined) return [ready, 'completed']
  for (const [suffix, state] of stageEndSuffixes)
    if (title.endsWith(suffix)) return [title.slice(0, -suffix.length).trimEnd(), state]
  return undefined
}

// levelRanks represents log level severity ranks
const levelRanks: Record<string, number> = { INFO: 1, WARN: 2, ERROR: 3 }

// severerLevel returns the more severe of two log levels
function severerLevel(current: string, next: string): string {
  return (levelRanks[next.toUpperCase()] ?? 0) > (levelRanks[current.toUpperCase()] ?? 0)
    ? next
    : current
}

// mergeStage merges an operational log entry into a stage card, preserving initial timestamp and taking most severe level
function mergeStage(row: StageRow, entry: AdminLog, detail: string): void {
  row.events.push(entry)
  row.entry = { ...row.events[0]!, level: severerLevel(row.entry.level, entry.level) }
  row.stage.detail = detail
}

// groupStage merges operational log entries into a stage card by source and stage name; returns false if entry does not belong to a stage card
function groupStage(rows: LogRow[], stages: Map<string, StageRow>, entry: AdminLog): boolean {
  const step = stagePattern.exec(entry.message)
  if (step) {
    const [, name = '', index, total, detail = ''] = step
    const key = `${entry.source}\n${name}`
    const open = stages.get(key)
    if (open && Number(index) > open.stage.step) {
      mergeStage(open, entry, detail)
      open.stage.step = Number(index)
      open.stage.steps = Number(total)
      return true
    }
    const row: StageRow = {
      key: entryKey(entry),
      entry,
      events: [entry],
      stage: { name, step: Number(index), steps: Number(total), detail, state: 'running' },
    }
    rows.push(row)
    stages.set(key, row)
    return true
  }
  const separator = entry.message.indexOf(' | ')
  const title = separator < 0 ? entry.message : entry.message.slice(0, separator)
  const open = stages.get(`${entry.source}\n${title}`)
  if (open) {
    mergeStage(open, entry, separator < 0 ? '' : entry.message.slice(separator + 3))
    return true
  }
  const end = stageEnd(title)
  if (end === undefined) return false
  const key = `${entry.source}\n${end[0]}`
  const ended = stages.get(key)
  if (ended === undefined) return false
  mergeStage(ended, entry, entry.message)
  ended.stage.state = end[1]
  stages.delete(key)
  return true
}

// groupLogs merges lifecycles by request ID and operational logs by stage and source, preserving original order of service events
export function groupLogs(logs: AdminLog[]): LogRow[] {
  const rows: LogRow[] = []
  const requests = new Map<string, LogRow>()
  const stages = new Map<string, StageRow>()
  for (const entry of logs) {
    const id = entry.request?.id
    const row = id ? requests.get(id) : undefined
    if (row) {
      row.events.push(entry)
      row.entry = { ...entry, request: { ...row.entry.request!, ...entry.request! } }
    } else if (id || entry.event !== 'runtime.message' || !groupStage(rows, stages, entry)) {
      const next = { key: id || entryKey(entry), entry, events: [entry] }
      rows.push(next)
      if (id) requests.set(id, next)
    }
  }
  return rows
}
