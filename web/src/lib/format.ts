import { formatDistanceToNow, parseISO } from "date-fns"

export function reltime(iso?: string): string {
  if (!iso) return "—"
  try { return formatDistanceToNow(parseISO(iso), { addSuffix: true }) } catch { return "—" }
}
