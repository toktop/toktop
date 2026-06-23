import { useCallback, useEffect, useRef, useState } from "react"
import { Link } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { useQueryClient } from "@tanstack/react-query"
import { X } from "lucide-react"

import { useLiveStatus, useSummary } from "@/api/queries"
import { useStream, type StreamStatus } from "@/api/useStream"
import type { LiveEvent, LiveSessionItem, Summary } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { LiveDot } from "@/components/live-dot"
import { reltime } from "@/lib/format"

// Unique per live session (source_id is per-provider, not per-session).
const sessionKey = (s: { source_id: string; session_id: string }) =>
  `${s.source_id}:${s.session_id}`

// The card's primary label is the session title; project name is shown
// separately and de-emphasized. Fall back to project/id only when untitled.
function cardLabel(s: LiveSessionItem): string {
  return s.title || s.project_name || s.external_session_id || s.session_id
}

// ── summary ────────────────────────────────────────────────────────────────────

function StatCard({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 text-card-foreground">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value.toLocaleString()}</p>
    </div>
  )
}

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

// ── session card (clickable → live detail) ──────────────────────────────────────

function SessionCard({ item, onOpen }: { item: LiveSessionItem; onOpen: () => void }) {
  const { t } = useTranslation()
  const label = cardLabel(item)
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex flex-col gap-2 rounded-lg border border-border bg-card p-4 text-start text-card-foreground transition-colors hover:border-ring/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {item.provider}
        </span>
        <StatusBadge status={item.current_status} />
      </div>
      <div className="min-w-0">
        <p className="truncate text-sm font-medium" title={label}>{label}</p>
        {item.project_name && item.project_name !== label && (
          <p className="truncate text-xs text-muted-foreground" title={item.project_name}>{item.project_name}</p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>{item.turn_count} {t("page.dashboard.card.turns")}</span>
        <span>{item.tool_call_count} {t("page.dashboard.card.tools")}</span>
        <span className="ml-auto shrink-0">{reltime(item.last_activity_at)}</span>
      </div>
    </button>
  )
}

// ── live detail dialog ──────────────────────────────────────────────────────────

function LiveDetailDialog({
  item, events, onClose,
}: { item: LiveSessionItem; events: LiveEvent[]; onClose: () => void }) {
  const { t }    = useTranslation()
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose() }
    document.addEventListener("keydown", onKey)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = "hidden"
    closeRef.current?.focus()
    return () => {
      document.removeEventListener("keydown", onKey)
      document.body.style.overflow = prevOverflow
    }
  }, [onClose])

  const label = cardLabel(item)
  const rows: [string, string | number][] = [
    [t("page.dashboard.detail.provider"),      item.provider],
    [t("page.dashboard.detail.sessionStatus"), item.session_status || "—"],
    [t("page.dashboard.detail.lastEvent"),     item.last_event_type || "—"],
    [t("page.dashboard.detail.turns"),         item.turn_count],
    [t("page.dashboard.detail.tools"),         item.tool_call_count],
    [t("page.dashboard.detail.started"),       reltime(item.started_at)],
    [t("page.dashboard.detail.lastActivity"),  reltime(item.last_activity_at)],
  ]

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true" aria-label={label}>
      <div className="absolute inset-0 bg-black/50" onClick={onClose} aria-hidden="true" />
      <div className="relative z-10 flex max-h-[85vh] w-full max-w-lg flex-col overflow-hidden rounded-lg border border-border bg-card shadow-xl">
        <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-xs font-medium uppercase tracking-wide text-muted-foreground">{item.provider}</span>
            <StatusBadge status={item.current_status} />
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            aria-label={t("page.dashboard.detail.close")}
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <X className="size-5" aria-hidden="true" />
          </button>
        </div>

        <div className="space-y-4 overflow-auto px-5 py-4">
          <div className="min-w-0">
            <p className="text-sm font-medium" title={label}>{label}</p>
            {item.project_name && item.project_name !== label && (
              <p className="truncate text-xs text-muted-foreground" title={item.project_name}>{item.project_name}</p>
            )}
          </div>

          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
            {rows.map(([k, v]) => (
              <div key={k} className="min-w-0">
                <dt className="text-xs text-muted-foreground">{k}</dt>
                <dd className="truncate">{v}</dd>
              </div>
            ))}
          </dl>

          {item.transcript_path && (
            <div className="min-w-0">
              <dt className="text-xs text-muted-foreground">{t("page.dashboard.detail.transcript")}</dt>
              <dd className="truncate font-mono text-xs" title={item.transcript_path}>{item.transcript_path}</dd>
            </div>
          )}

          {/* live event feed for this session */}
          <div className="space-y-1.5">
            <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
              {t("page.dashboard.detail.events")}
            </div>
            {events.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("page.dashboard.detail.noEvents")}</p>
            ) : (
              <ul className="divide-y divide-border rounded-md border border-border bg-muted/30">
                {events.map((e, i) => (
                  <li key={e.event_id ?? i} className="flex items-center gap-2 px-3 py-1.5 text-xs">
                    <span className="font-mono text-foreground">{e.type}</span>
                    {e.status && <StatusBadge status={e.status} />}
                    {e.reason && <span className="truncate text-muted-foreground">{e.reason}</span>}
                    <span className="ml-auto shrink-0 text-muted-foreground">{reltime(e.at)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <Link to={`/sessions/${item.session_id}`} className="inline-block text-sm text-primary hover:underline">
            {t("page.dashboard.detail.viewSession")} →
          </Link>
        </div>
      </div>
    </div>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

export function DashboardPage() {
  const { t } = useTranslation()
  const qc    = useQueryClient()

  const [events, setEvents]           = useState<LiveEvent[]>([])
  const [selectedKey, setSelectedKey] = useState<string | null>(null)

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

  // each live event: keep a bounded recent buffer (for the per-session feed) and
  // trigger the debounced refetch.
  const onEvent = useCallback((ev: LiveEvent) => {
    setEvents((prev) => [ev, ...prev].slice(0, 100))
    debouncedInvalidate()
  }, [debouncedInvalidate])

  // keeping this mounted holds the SSE connection open (also resets the daemon
  // idle-stop timer); returns the connection status for the live indicator.
  const streamStatus = useStream(onEvent, { onResync: debouncedInvalidate })

  const { data: statusData, isLoading: statusLoading, error: statusError } = useLiveStatus()
  const { data: summary, isLoading: summaryLoading }                       = useSummary()
  const isLoading = statusLoading || summaryLoading

  // Derive the selected session from the live data each render so the dialog's
  // fields update in real time; null when it drops out of the live list.
  const selected = selectedKey && statusData
    ? statusData.items.find((i) => sessionKey(i) === selectedKey) ?? null
    : null
  const selectedEvents = selectedKey
    ? events.filter((e) => `${e.source_id}:${e.session_id}` === selectedKey)
    : []

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">{t("page.dashboard.title")}</h1>
        <LiveIndicator status={streamStatus} lastAt={events[0]?.at} />
      </div>

      {summary && <SummaryBand summary={summary} />}

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
              <SessionCard
                key={sessionKey(item)}
                item={item}
                onOpen={() => setSelectedKey(sessionKey(item))}
              />
            ))}
          </div>
        )}
      </section>

      {selected && (
        <LiveDetailDialog
          item={selected}
          events={selectedEvents}
          onClose={() => setSelectedKey(null)}
        />
      )}
    </div>
  )
}
