import { Link } from "react-router-dom"
import { useTranslation } from "react-i18next"

import type { LiveEvent } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { useOverflowTooltip } from "@/components/overflow-tooltip"
import { clockTime } from "@/lib/format"

// RecentEvents is a compact, capped preview of the live event feed, shared by the
// dashboard (global) and the session detail (scoped to one session). The full,
// interactive list (pause / filter) lives on the events page at viewAllHref.
export function RecentEvents({
  events,
  viewAllHref,
  max = 8,
  showSession = true,
}: {
  events: LiveEvent[]
  viewAllHref: string
  max?: number
  showSession?: boolean
}) {
  const { t } = useTranslation()
  const tip   = useOverflowTooltip()
  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{t("events.recent")}</h2>
        <Link to={viewAllHref} className="shrink-0 text-xs text-primary hover:underline">
          {t("events.viewAll")} →
        </Link>
      </div>
      {events.length === 0 ? (
        <p className="rounded-md border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
          {t("events.none")}
        </p>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border bg-card">
          {events.slice(0, max).map((e, i) => (
            <li key={e.event_id ?? i} className="flex items-center gap-2 px-3 py-1.5 text-xs">
              <span className="font-mono text-foreground">{e.type}</span>
              {e.status && <StatusBadge status={e.status} />}
              {showSession && e.session_id && (
                <Link
                  to={`/sessions/${e.session_id}`}
                  className="min-w-0 truncate text-muted-foreground hover:underline"
                  {...tip}
                >
                  {e.project_name || e.session_id}
                </Link>
              )}
              {e.reason && <span className="min-w-0 truncate text-muted-foreground" {...tip}>{e.reason}</span>}
              <span className="ml-auto shrink-0 font-mono text-muted-foreground">{clockTime(e.at)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
