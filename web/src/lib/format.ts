import { format, formatDistanceToNow, parseISO } from "date-fns"
import type { Tokens } from "@/api/types"

export function reltime(iso?: string): string {
  if (!iso) return "—"
  try { return formatDistanceToNow(parseISO(iso), { addSuffix: true }) } catch { return "—" }
}

// clockTime is for high-frequency streams (the events feed) where every row is
// "less than a minute ago": a precise local wall-clock time with milliseconds so
// rows are distinguishable and ordered.
export function clockTime(iso?: string): string {
  if (!iso) return "—"
  try { return format(parseISO(iso), "HH:mm:ss.SSS") } catch { return "—" }
}

export function totalTokens(t: Tokens): number {
  return (t.input_tokens ?? 0) + (t.output_tokens ?? 0)
}

export function fmtTokens(t: Tokens): string {
  const n = totalTokens(t)
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000)     return `${(n / 1_000).toFixed(1)}K`
  return n.toString()
}

export function fmtMs(ms?: number): string {
  if (ms == null) return "—"
  if (ms < 1_000)       return `${ms}ms`
  if (ms < 60_000)      return `${(ms / 1_000).toFixed(1)}s`
  if (ms < 3_600_000)   return `${Math.round(ms / 60_000)}m`
  return `${(ms / 3_600_000).toFixed(1)}h`
}

// fmtNum renders an integer with locale thousands separators — the one place
// plain counts are formatted, shared across the dashboard and analytics tables.
export function fmtNum(n: number): string {
  return n.toLocaleString()
}

// topN ranks items by a numeric metric (descending, positive only) and projects
// them to the {label,value} shape the bar charts consume — shared by the dashboard
// and analytics insight charts so the cutoff/sort/filter can't drift between them.
export function topN<T>(
  items: T[] | undefined,
  value: (x: T) => number,
  label: (x: T) => string,
  n = 7,
): { label: string; value: number }[] {
  return [...(items ?? [])]
    .filter((x) => value(x) > 0)
    .sort((a, b) => value(b) - value(a))
    .slice(0, n)
    .map((x) => ({ label: label(x), value: value(x) }))
}
