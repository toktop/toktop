import { useCallback, useMemo, useRef, useState } from "react"
import { Link, useSearchParams } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { Pause, Play } from "lucide-react"

import { useStream } from "@/api/useStream"
import { useSession } from "@/api/queries"
import type { LiveEvent } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { LiveDot } from "@/components/live-dot"
import { VirtualTable } from "@/components/virtual-table"
import type { Column } from "@/components/virtual-table"
import { useOverflowTooltip } from "@/components/overflow-tooltip"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { clockTime } from "@/lib/format"

// Cap the in-memory feed; this is a live tail, not a history store (the event log
// on disk is the system of record).
const CAP = 300

export function EventsPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const sessionFilter = searchParams.get("session") ?? ""
  const [events, setEvents]         = useState<LiveEvent[]>([])
  const [paused, setPaused]         = useState(false)
  const [pendingCount, setPending]  = useState(0)
  const [typeFilter, setTypeFilter] = useState("")
  const pendingRef = useRef<LiveEvent[]>([])
  // The detail cell's file clips inside its own flex layout (the td doesn't
  // overflow), so it reports overflow itself; every other column rides the td.
  const tip = useOverflowTooltip()

  // A live event's session_id is the internal id on enriched events but the
  // provider's external id on raw hook events, and the ?session= param may be
  // either form. Resolve the session (cache-shared with the session page) and match
  // events against both its internal and external ids on both event fields, so a
  // session-scoped feed actually catches its events.
  const { data: sessData } = useSession(sessionFilter)
  const resolved = !sessionFilter || sessData !== undefined
  const sessionIds = useMemo(() => {
    if (!sessionFilter) return [] as string[]
    const s = sessData?.session
    return [s?.id, s?.external_id, sessionFilter].filter((v): v is string => !!v)
  }, [sessData, sessionFilter])
  const matchesSession = useCallback(
    (e: LiveEvent) =>
      sessionIds.length === 0 ||
      sessionIds.includes(e.session_id ?? "") ||
      sessionIds.includes(e.external_session_id ?? ""),
    [sessionIds],
  )

  // Filter by session at intake once it resolves (a scoped feed only cares about
  // one session), so the buffer stays scoped and the 300-cap isn't diluted by other
  // sessions. Before it resolves the id mapping is unknown (an event may carry only
  // the external id while ?session= is the internal id), so keep events
  // optimistically — the render-time matchesSession filter re-evaluates the buffer
  // once it resolves, so the earliest events aren't lost. While paused, buffer
  // incoming events (nothing lost up to CAP); the count tracks the real buffer
  // length, not an unbounded tally; resume flushes them.
  const onEvent = useCallback((e: LiveEvent) => {
    if (resolved && !matchesSession(e)) return
    if (paused) {
      pendingRef.current = [e, ...pendingRef.current].slice(0, CAP)
      setPending(pendingRef.current.length)
    } else {
      setEvents((prev) => [e, ...prev].slice(0, CAP))
    }
  }, [paused, resolved, matchesSession])

  const streamStatus = useStream(onEvent)

  const resume = useCallback(() => {
    // Capture the buffer before clearing: the setEvents updater runs later, so it
    // must close over a local, not read pendingRef.current after we empty it.
    const buffered = pendingRef.current
    pendingRef.current = []
    setEvents((prev) => [...buffered, ...prev].slice(0, CAP))
    setPending(0)
    setPaused(false)
  }, [])

  // Scope the buffer to the session at render too, so events kept optimistically
  // before the session resolved are re-evaluated once the id mapping is known.
  const sessionEvents = sessionFilter ? events.filter(matchesSession) : events
  const types = [...new Set(sessionEvents.map((e) => e.type))].sort()
  // `items` lets base-ui's <SelectValue> show the chosen label (e.g. "All types"),
  // not the raw value ("all"); the sentinel + each type label live here once. Keep
  // the active filter selectable even after its last event scrolls out of the
  // capped buffer, so the Select can't orphan to a blank trigger + empty feed.
  const typeItems: Record<string, string> = {
    all: t("page.events.allTypes"),
    ...Object.fromEntries(types.map((ty) => [ty, ty])),
    ...(typeFilter && !types.includes(typeFilter) ? { [typeFilter]: typeFilter } : {}),
  }
  // Sort by event time (desc) so the column reads strictly newest-first; events
  // can arrive slightly out of timestamp order across providers/pipeline stages.
  // (Session scoping is applied at intake; only the type refinement remains here.)
  const filtered = sessionEvents.filter((e) => !typeFilter || e.type === typeFilter)
  const shown = [...filtered].sort((a, b) => (b.at ?? "").localeCompare(a.at ?? ""))

  // Single-line columns for the virtualized feed: every row is one fixed-height
  // line (cells truncate), matching the analytics tables.
  const columns: Column<LiveEvent>[] = useMemo(() => [
    {
      header: t("page.events.col.time"),
      width:  "w-24",
      cell:   (e) => <span className="font-mono text-xs text-muted-foreground" title={e.at}>{clockTime(e.at)}</span>,
    },
    {
      header: t("page.events.col.type"),
      width:  "w-44",
      cell:   (e) => <span className="font-mono text-xs">{e.type}</span>,
    },
    {
      header: t("page.events.col.provider"),
      width:  "w-28",
      cell:   (e) => <span className="text-xs text-muted-foreground">{e.provider || "—"}</span>,
    },
    {
      header: t("page.events.col.session"),
      width:  "w-56",
      cell:   (e) =>
        e.session_id ? (
          <Link
            to={`/sessions/${e.session_id}`}
            className="font-mono text-xs text-primary hover:underline"
          >
            {e.project_name || e.session_id}
          </Link>
        ) : <span className="text-xs text-muted-foreground">—</span>,
    },
    {
      header: t("page.events.col.detail"),
      width:  "",
      cell:   (e) => (
        <span className="flex min-w-0 items-center gap-2 text-xs">
          {e.status && <StatusBadge status={e.status} />}
          {e.reason && <span className="shrink-0 text-muted-foreground">{e.reason}</span>}
          {e.file && <span className="min-w-0 truncate font-mono text-muted-foreground" {...tip}>{e.file}</span>}
        </span>
      ),
    },
  ], [t, tip])

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">{t("page.events.title")}</h1>
        <LiveDot status={streamStatus} />
      </div>

      <p className="text-sm text-muted-foreground">{t("page.events.subtitle")}</p>

      {sessionFilter && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-xs">
          <span className="shrink-0 text-muted-foreground">{t("page.events.sessionScope")}</span>
          <code className="min-w-0 truncate font-mono text-foreground">{sessionFilter}</code>
          <Link to="/events" className="ml-auto shrink-0 text-primary hover:underline">{t("page.events.showAll")}</Link>
        </div>
      )}

      {/* toolbar */}
      <div className="flex flex-wrap items-center gap-3">
        <Select items={typeItems} value={typeFilter || "all"} onValueChange={(v) => setTypeFilter(v === "all" ? "" : (v as string))}>
          <SelectTrigger size="sm" className="w-48" aria-label={t("page.events.filterType")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {Object.entries(typeItems).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
          </SelectContent>
        </Select>

        <button
          type="button"
          onClick={() => (paused ? resume() : setPaused(true))}
          className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border px-3 text-sm text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {paused
            ? <><Play  className="size-4" aria-hidden="true" />{t("page.events.resume", { count: pendingCount })}</>
            : <><Pause className="size-4" aria-hidden="true" />{t("page.events.pause")}</>}
        </button>

        <span className="ml-auto text-xs text-muted-foreground">{t("page.events.count", { n: shown.length })}</span>
      </div>

      {/* feed */}
      {shown.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground">
          {t("page.events.empty")}
        </p>
      ) : (
        <VirtualTable
          columns={columns}
          rows={shown}
          rowKey={(e, i) => e.event_id ?? String(i)}
          minWidth="min-w-[44rem]"
        />
      )}
    </div>
  )
}
