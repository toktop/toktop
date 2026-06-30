import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Link } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { useQueryClient } from "@tanstack/react-query"

import { useLiveStatus, useProjects, useSummary, useTools } from "@/api/queries"
import { useStream, type StreamStatus } from "@/api/useStream"
import type { LiveEvent, LiveSessionItem, Summary } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { LiveDot } from "@/components/live-dot"
import { RecentEvents } from "@/components/recent-events"
import { BarChartCard } from "@/components/bar-chart-card"
import { useOverflowTooltip } from "@/components/overflow-tooltip"
import { reltime, fmtNum, topN } from "@/lib/format"

// The dashboard is a live overview, not a session browser: it highlights the most
// recently-active sessions (capped) and the live event pulse; browsing/filtering
// all sessions is the /sessions page's job.
const MAX_SESSIONS = 6

const sessionKey = (s: { source_id: string; session_id: string }) =>
  `${s.source_id}:${s.session_id}`

// Lead with the session title; the project name is shown de-emphasized.
function cardLabel(s: LiveSessionItem): string {
  return s.title || s.project_name || s.external_session_id || s.session_id
}

// ── summary ────────────────────────────────────────────────────────────────────

function StatCard({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 text-card-foreground">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{typeof value === "number" ? fmtNum(value) : value}</p>
    </div>
  )
}

const SummaryBand = memo(function SummaryBand({ summary }: { summary: Summary }) {
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
})

// ── insights charts ────────────────────────────────────────────────────────────

// Two at-a-glance rankings over the same aggregates the analytics tab serves:
// which projects you spend turns in, and which tools get called most. Reuses the
// cached projects/tools queries — no extra endpoint.
const InsightsCharts = memo(function InsightsCharts() {
  const { t }              = useTranslation()
  const { data: projects } = useProjects()
  const { data: tools }    = useTools()

  const topProjects = useMemo(() => topN(projects, (p) => p.turn_count, (p) => p.name), [projects])
  const topTools    = useMemo(() => topN(tools, (tc) => tc.call_count, (tc) => tc.name), [tools])

  return (
    <section className="space-y-2" aria-label={t("page.dashboard.charts.label")}>
      <h2 className="text-sm font-semibold">{t("page.dashboard.charts.label")}</h2>
      <div className="grid gap-4 lg:grid-cols-2">
        <BarChartCard
          title={t("page.dashboard.charts.topProjects")}
          metricLabel={t("page.dashboard.charts.byTurns")}
          data={topProjects}
          emptyText={t("page.dashboard.charts.empty")}
        />
        <BarChartCard
          title={t("page.dashboard.charts.topTools")}
          metricLabel={t("page.dashboard.charts.byCalls")}
          data={topTools}
          emptyText={t("page.dashboard.charts.empty")}
        />
      </div>
    </section>
  )
})

// ── live indicator ─────────────────────────────────────────────────────────────

function LiveIndicator({ status, lastAt }: { status: StreamStatus; lastAt?: string }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <LiveDot status={status} />
      {lastAt && <span>· {t("page.dashboard.updated", { time: reltime(lastAt) })}</span>}
    </div>
  )
}

// ── session card → the canonical session detail (same target as the /sessions list) ──

const SessionCard = memo(function SessionCard({ item }: { item: LiveSessionItem }) {
  const { t } = useTranslation()
  const tip   = useOverflowTooltip()
  const label = cardLabel(item)
  return (
    <Link
      to={`/sessions/${item.session_id}`}
      className="flex flex-col gap-2 rounded-lg border border-border bg-card p-4 text-card-foreground transition-colors hover:border-ring/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {item.provider}
        </span>
        <StatusBadge status={item.current_status} />
      </div>
      <div className="min-w-0">
        <p className="truncate text-sm font-medium" {...tip}>{label}</p>
        {item.project_name && item.project_name !== label && (
          <p className="truncate text-xs text-muted-foreground" {...tip}>{item.project_name}</p>
        )}
      </div>
      <div className="mt-auto flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>{item.turn_count} {t("page.dashboard.card.turns")}</span>
        <span>{item.tool_call_count} {t("page.dashboard.card.tools")}</span>
        <span className="ml-auto shrink-0">{reltime(item.last_activity_at)}</span>
      </div>
    </Link>
  )
})

// ── page ──────────────────────────────────────────────────────────────────────

export function DashboardPage() {
  const { t } = useTranslation()
  const qc    = useQueryClient()

  const [events, setEvents] = useState<LiveEvent[]>([])

  // debounced invalidation — a burst of events triggers one refetch
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedInvalidate = useCallback(() => {
    if (timerRef.current !== null) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => {
      void qc.invalidateQueries({ queryKey: ["status"] })
      void qc.invalidateQueries({ queryKey: ["summary"] })
    }, 250)
  }, [qc])
  useEffect(() => () => { if (timerRef.current !== null) clearTimeout(timerRef.current) }, [])

  // each live event: keep a bounded recent buffer (for the live events preview)
  // and trigger the debounced refetch.
  const onEvent = useCallback((ev: LiveEvent) => {
    setEvents((prev) => [ev, ...prev].slice(0, 100))
    debouncedInvalidate()
  }, [debouncedInvalidate])

  // keeping this mounted holds the SSE connection open (also resets the daemon
  // idle-stop timer); returns the connection status for the live indicator.
  const streamStatus = useStream(onEvent, { onResync: debouncedInvalidate })

  const { data: statusData, isLoading: statusLoading, error: statusError } = useLiveStatus()
  const { data: summary }                                                  = useSummary()

  // Most recently active sessions, capped — the dashboard highlight. Memoized on
  // statusData so the per-event re-render (setEvents) doesn't re-sort the full
  // live-status array; it only changes when the status query refetches.
  const sessions = useMemo(
    () => [...(statusData?.items ?? [])]
      .sort((a, b) => (b.last_activity_at ?? "").localeCompare(a.last_activity_at ?? ""))
      .slice(0, MAX_SESSIONS),
    [statusData],
  )

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">{t("page.dashboard.title")}</h1>
        <LiveIndicator status={streamStatus} lastAt={events[0]?.at} />
      </div>

      {summary && <SummaryBand summary={summary} />}

      <InsightsCharts />

      {/* live sessions highlight — full list/filtering lives on /sessions */}
      <section className="space-y-2" aria-label={t("page.dashboard.sessions.label")}>
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">{t("page.dashboard.sessions.label")}</h2>
          <Link to="/sessions" className="shrink-0 text-xs text-primary hover:underline">
            {t("page.dashboard.viewAllSessions")} →
          </Link>
        </div>

        {statusLoading && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}

        {statusError && (
          <p className="text-sm text-destructive" role="alert">
            {statusError.message ?? t("common.error")}
          </p>
        )}

        {!statusLoading && !statusError && sessions.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("page.dashboard.empty")}</p>
        )}

        {sessions.length > 0 && (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {sessions.map((item) => (
              <SessionCard key={sessionKey(item)} item={item} />
            ))}
          </div>
        )}
      </section>

      {/* live event pulse */}
      <RecentEvents events={events} viewAllHref="/events" max={8} />
    </div>
  )
}
