import { useCallback, useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"
import { useQueryClient } from "@tanstack/react-query"

import { useLiveStatus, useSummary } from "@/api/queries"
import { useStream } from "@/api/useStream"
import type { LiveSessionItem, Summary } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { reltime } from "@/lib/format"

// ── summary stat card ─────────────────────────────────────────────────────────

function StatCard({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 text-card-foreground">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value.toLocaleString()}</p>
    </div>
  )
}

// ── summary band ─────────────────────────────────────────────────────────────

function SummaryBand({ summary }: { summary: Summary }) {
  const { t } = useTranslation()
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <StatCard label={t("page.dashboard.stat.sessions")} value={summary.sessions} />
      <StatCard label={t("page.dashboard.stat.turns")}    value={summary.turns} />
      <StatCard label={t("page.dashboard.stat.tools")}    value={summary.tool_calls} />
      <StatCard label={t("page.dashboard.stat.tokens")}
                value={summary.input_tokens + summary.output_tokens} />
    </div>
  )
}

// ── session card ──────────────────────────────────────────────────────────────

function SessionCard({ item }: { item: LiveSessionItem }) {
  const { t } = useTranslation()
  const label = item.project_name ?? item.title ?? item.external_session_id ?? item.session_id

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-card p-4 text-card-foreground transition-colors hover:border-ring/40">
      {/* top row: provider badge + status badge */}
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {item.provider}
        </span>
        <StatusBadge status={item.current_status} />
      </div>

      {/* project / title */}
      <p className="truncate text-sm font-medium" title={label}>
        {label}
      </p>

      {/* meta row */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>
          {item.turn_count} {t("page.dashboard.card.turns")}
        </span>
        <span>
          {item.tool_call_count} {t("page.dashboard.card.tools")}
        </span>
        <span className="ml-auto shrink-0">{reltime(item.last_activity_at)}</span>
      </div>
    </div>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

export function DashboardPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()

  // debounced invalidation — burst of events → single refetch
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedInvalidate = useCallback(() => {
    if (timerRef.current !== null) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => {
      void qc.invalidateQueries({ queryKey: ["status"] })
      void qc.invalidateQueries({ queryKey: ["summary"] })
    }, 250)
  }, [qc])

  // clear any pending debounce timer on unmount
  useEffect(() => () => { if (timerRef.current !== null) clearTimeout(timerRef.current) }, [])

  // wire live stream → cache invalidation; keeping this mounted keeps the SSE
  // connection open (which also resets the daemon idle-stop timer)
  useStream(debouncedInvalidate, { onResync: debouncedInvalidate })

  const { data: statusData, isLoading: statusLoading, error: statusError } = useLiveStatus()
  const { data: summary, isLoading: summaryLoading }                       = useSummary()

  const isLoading = statusLoading || summaryLoading

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold">{t("page.dashboard.title")}</h1>

      {/* summary stats */}
      {summary && <SummaryBand summary={summary} />}

      {/* session list */}
      <section aria-label={t("page.dashboard.sessions.label")}>
        {isLoading && (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        )}

        {statusError && (
          <p className="text-sm text-destructive" role="alert">
            {statusError.message ?? t("common.error")}
          </p>
        )}

        {!isLoading && !statusError && statusData && statusData.items.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("page.dashboard.empty")}</p>
        )}

        {statusData && statusData.items.length > 0 && (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {statusData.items.map((item) => (
              <SessionCard key={item.source_id} item={item} />
            ))}
          </div>
        )}
      </section>
    </div>
  )
}
