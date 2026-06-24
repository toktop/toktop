import { useCallback, useMemo, useRef, useState } from "react"
import { Link, useSearchParams } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { Pause, Play } from "lucide-react"

import { useStream } from "@/api/useStream"
import { useSession } from "@/api/queries"
import type { LiveEvent } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { LiveDot } from "@/components/live-dot"
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

  // A live event's session_id is the internal id on enriched events but the
  // provider's external id on raw hook events, and the ?session= param may be
  // either form. Resolve the session (cache-shared with the session page) and match
  // events against both its internal and external ids on both event fields, so a
  // session-scoped feed actually catches its events.
  const { data: sessData } = useSession(sessionFilter)
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

  // Filter by session at intake (a scoped feed only cares about one session), so
  // the visible list AND the paused count reflect only this session — not a global
  // tally. While paused, buffer incoming events (nothing lost); resume flushes them.
  const onEvent = useCallback((e: LiveEvent) => {
    if (!matchesSession(e)) return
    if (paused) {
      pendingRef.current = [e, ...pendingRef.current].slice(0, CAP)
      setPending((c) => c + 1)
    } else {
      setEvents((prev) => [e, ...prev].slice(0, CAP))
    }
  }, [paused, matchesSession])

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

  const types = [...new Set(events.map((e) => e.type))].sort()
  // `items` lets base-ui's <SelectValue> show the chosen label (e.g. "All types"),
  // not the raw value ("all"); the sentinel + each type label live here once.
  const typeItems: Record<string, string> = { all: t("page.events.allTypes"), ...Object.fromEntries(types.map((ty) => [ty, ty])) }
  // Sort by event time (desc) so the column reads strictly newest-first; events
  // can arrive slightly out of timestamp order across providers/pipeline stages.
  // (Session scoping is applied at intake; only the type refinement remains here.)
  const filtered = events.filter((e) => !typeFilter || e.type === typeFilter)
  const shown = [...filtered].sort((a, b) => (b.at ?? "").localeCompare(a.at ?? ""))

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
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="border-b border-border bg-muted/50 text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 text-left font-medium">{t("page.events.col.time")}</th>
                <th scope="col" className="px-3 py-2 text-left font-medium">{t("page.events.col.type")}</th>
                <th scope="col" className="px-3 py-2 text-left font-medium">{t("page.events.col.provider")}</th>
                <th scope="col" className="px-3 py-2 text-left font-medium">{t("page.events.col.session")}</th>
                <th scope="col" className="px-3 py-2 text-left font-medium">{t("page.events.col.detail")}</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((e, i) => (
                <tr key={e.event_id ?? i} className="border-b border-border align-top last:border-0">
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs text-muted-foreground" title={e.at}>{clockTime(e.at)}</td>
                  <td className="px-3 py-2"><span className="font-mono text-xs">{e.type}</span></td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">{e.provider || "—"}</td>
                  <td className="px-3 py-2 text-xs">
                    {e.session_id ? (
                      <Link
                        to={`/sessions/${e.session_id}`}
                        className="font-mono text-primary hover:underline"
                        title={e.project_name || e.session_id}
                      >
                        {(e.project_name || e.session_id).slice(0, 32)}
                      </Link>
                    ) : "—"}
                  </td>
                  <td className="px-3 py-2 text-xs">
                    <div className="flex flex-wrap items-center gap-2">
                      {e.status && <StatusBadge status={e.status} />}
                      {e.reason && <span className="text-muted-foreground">{e.reason}</span>}
                      {e.file && (
                        <span className="max-w-[18rem] truncate font-mono text-muted-foreground" title={e.file}>{e.file}</span>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
