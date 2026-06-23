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
