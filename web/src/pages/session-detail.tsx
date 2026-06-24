import { useCallback, useEffect, useRef, useState } from "react"
import { useParams, useSearchParams, Link } from "react-router-dom"
import { useTranslation }              from "react-i18next"
import { useQueryClient }              from "@tanstack/react-query"
import { Tabs }                        from "@base-ui/react/tabs"
import { X }                           from "lucide-react"
import ReactMarkdown                   from "react-markdown"
import remarkGfm                       from "remark-gfm"

import { useSession, useHandoff }      from "@/api/queries"
import { useStream }                   from "@/api/useStream"
import type { AgentRun, LiveEvent, Turn } from "@/api/types"
import { StatusBadge }                 from "@/components/status-badge"
import { LiveDot }                     from "@/components/live-dot"
import { RecentEvents }                from "@/components/recent-events"
import { reltime, fmtTokens, fmtMs }   from "@/lib/format"

// ── helpers ───────────────────────────────────────────────────────────────────

function fmtBytes(n?: number): string {
  if (!n) return "—"
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)} MiB`
  if (n >= 1_024)     return `${(n / 1_024).toFixed(1)} KiB`
  return `${n} B`
}

// ── turn row ──────────────────────────────────────────────────────────────────

function TurnRow({ turn, highlight }: { turn: Turn; highlight?: boolean }) {
  const { t }           = useTranslation()
  const [open, setOpen] = useState(false)
  const closeRef        = useRef<HTMLButtonElement>(null)
  const rowRef          = useRef<HTMLButtonElement>(null)

  // Deep-link target (e.g. from the analytics failed/rejected drill-down): scroll
  // the turn into view and flag it so the user lands on the exact failing turn.
  useEffect(() => {
    if (highlight) rowRef.current?.scrollIntoView({ block: "center" })
  }, [highlight])

  const userText = turn.user_message?.trim()    || ""
  const asstText = turn.assistant_final?.trim() || ""
  const tokenStr = fmtTokens(turn.tokens)
  const hasTools = turn.tool_call_count > 0
  const label    = t("page.session.turns.index", { n: turn.index + 1 })

  // Dialog a11y: Esc closes, body scroll locks, focus moves to the close button
  // (and returns to the trigger on close).
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false) }
    document.addEventListener("keydown", onKey)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = "hidden"
    closeRef.current?.focus()
    return () => {
      document.removeEventListener("keydown", onKey)
      document.body.style.overflow = prevOverflow
    }
  }, [open])

  const meta = (
    <div className="flex flex-wrap items-center gap-2">
      <span className="font-mono text-xs text-muted-foreground">{label}</span>
      <StatusBadge status={turn.status} />
      {tokenStr !== "0" && (
        <span className="text-xs text-muted-foreground">{t("page.session.turns.tokens", { n: tokenStr })}</span>
      )}
      {hasTools && (
        <span className="text-xs text-muted-foreground">{t("page.session.turns.tools", { n: turn.tool_call_count })}</span>
      )}
    </div>
  )

  return (
    <>
      {/* preview row — opens the full turn in a dialog */}
      <button
        ref={rowRef}
        type="button"
        onClick={() => setOpen(true)}
        className={`block w-full space-y-1.5 rounded-lg border bg-card px-4 py-3 text-start transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${highlight ? "border-ring ring-2 ring-ring/40" : "border-border"}`}
      >
        {meta}
        {userText && (
          <p className="line-clamp-2 text-sm leading-snug text-foreground/80">
            <span className="mr-1 text-xs uppercase tracking-wide text-muted-foreground">{t("page.session.turns.user")}</span>
            {userText}
          </p>
        )}
        {asstText && (
          <p className="line-clamp-2 text-sm leading-snug text-foreground">
            <span className="mr-1 text-xs uppercase tracking-wide text-muted-foreground">{t("page.session.turns.assistant")}</span>
            {asstText}
          </p>
        )}
      </button>

      {/* full-turn dialog */}
      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true" aria-label={label}>
          <div className="absolute inset-0 bg-black/50" onClick={() => setOpen(false)} aria-hidden="true" />
          <div className="relative z-10 flex max-h-[85vh] w-full max-w-2xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-xl">
            <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-3">
              {meta}
              <button
                ref={closeRef}
                type="button"
                onClick={() => setOpen(false)}
                aria-label={t("page.session.turns.close")}
                className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                <X className="size-5" aria-hidden="true" />
              </button>
            </div>

            <div className="space-y-5 overflow-auto px-5 py-4">
              {userText && (
                <div className="space-y-1">
                  <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{t("page.session.turns.user")}</div>
                  <p className="whitespace-pre-wrap text-sm leading-relaxed text-foreground/90">{userText}</p>
                </div>
              )}
              {asstText && (
                <div className="space-y-1">
                  <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{t("page.session.turns.assistant")}</div>
                  <p className="whitespace-pre-wrap text-sm leading-relaxed text-foreground">{asstText}</p>
                </div>
              )}
              {hasTools && turn.tool_calls && turn.tool_calls.length > 0 && (
                <div className="space-y-2">
                  <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                    {t("page.session.turns.tools", { n: turn.tool_call_count })}
                  </div>
                  <div className="divide-y divide-border rounded-md border border-border bg-muted/30">
                    {turn.tool_calls.map((tc) => (
                      <div key={tc.id} className="px-3 py-2 text-xs">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono font-medium text-foreground">{tc.name}</span>
                          <StatusBadge status={tc.status} />
                          {tc.duration_ms != null && <span className="text-muted-foreground">{fmtMs(tc.duration_ms)}</span>}
                        </div>
                        {tc.input && (
                          <pre className="mt-1 max-h-48 overflow-auto rounded bg-background p-2 text-[11px] text-muted-foreground whitespace-pre-wrap break-all">{tc.input}</pre>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  )
}

// ── agent run row ─────────────────────────────────────────────────────────────

function AgentRunRow({ run }: { run: AgentRun }) {
  const { t } = useTranslation()

  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 space-y-1.5">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="font-mono text-sm font-medium text-foreground">
          {run.type ?? run.tool}
        </span>
        <StatusBadge status={run.status} />
        {run.duration_ms != null && (
          <span className="text-xs text-muted-foreground">{fmtMs(run.duration_ms)}</span>
        )}
        {run.output_bytes != null && run.output_bytes > 0 && (
          <span className="text-xs text-muted-foreground">{fmtBytes(run.output_bytes)}</span>
        )}
      </div>
      {run.description && (
        <p className="text-sm text-foreground/80 line-clamp-2">{run.description}</p>
      )}
      {/* source provenance */}
      <p className="text-[11px] font-mono text-muted-foreground truncate">
        {t("page.session.handoff.source")}: {run.source.provider}/{run.source.session_id}
        {run.source.turn_id && `/${run.source.turn_id}`}
      </p>
    </div>
  )
}

// ── turns tab ─────────────────────────────────────────────────────────────────

function TurnsTab({ turns, highlightTurnId }: { turns: Turn[]; highlightTurnId?: string }) {
  const { t } = useTranslation()
  if (turns.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("page.session.turns.empty")}</p>
  }
  return (
    <div className="space-y-3">
      {turns.map((turn) => <TurnRow key={turn.id} turn={turn} highlight={turn.id === highlightTurnId} />)}
    </div>
  )
}

// ── handoff tab ───────────────────────────────────────────────────────────────

function HandoffTab({ sessionId }: { sessionId: string }) {
  const { t }                       = useTranslation()
  const { data, isLoading, error }  = useHandoff(sessionId)

  if (isLoading) {
    return <p className="text-sm text-muted-foreground">{t("page.session.handoff.loading")}</p>
  }
  if (error || !data) {
    return (
      <p className="text-sm text-destructive" role="alert">
        {error?.message ?? t("page.session.handoff.error")}
      </p>
    )
  }

  const { manifest, agent_runs, digest } = data

  return (
    <div className="space-y-6">
      {/* manifest header */}
      <div className="rounded-lg border border-border bg-card px-4 py-3 space-y-3">
        <div className="flex items-center gap-3 flex-wrap">
          <span className="text-xs uppercase tracking-wide text-muted-foreground">
            {t("page.session.handoff.workflowStatus")}
          </span>
          <StatusBadge status={manifest.workflow_status} />
        </div>

        {/* tally grid */}
        <div className="grid grid-cols-3 gap-x-6 gap-y-1 text-sm sm:grid-cols-6">
          {[
            ["completed",  manifest.completed_agent_runs],
            ["failed",     manifest.failed_agent_runs],
            ["interrupted",manifest.interrupted_agent_runs],
            ["incomplete", manifest.incomplete_agent_runs],
            ["rejected",   manifest.rejected_agent_runs],
          ].map(([key, n]) => (
            <div key={key as string} className="flex flex-col items-center">
              <span className="text-lg font-semibold tabular-nums">{n as number}</span>
              <span className="text-[11px] text-muted-foreground">
                {t(`page.session.handoff.${key as string}`)}
              </span>
            </div>
          ))}
          <div className="flex flex-col items-center">
            <span className="text-lg font-semibold tabular-nums">{manifest.agent_runs}</span>
            <span className="text-[11px] text-muted-foreground">
              {t("page.session.handoff.agentRuns")}
            </span>
          </div>
        </div>
      </div>

      {/* agent runs list */}
      {agent_runs.length > 0 ? (
        <div className="space-y-2">
          {agent_runs.map((run) => <AgentRunRow key={run.id} run={run} />)}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          {t("page.session.handoff.noAgentRuns")}
        </p>
      )}

      {/* digest markdown — the headline feature */}
      <div>
        <h3 className="mb-3 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
          {t("page.session.handoff.digest")}
        </h3>
        {digest ? (
          <div className="prose prose-sm max-w-none dark:prose-invert rounded-lg border border-border bg-card px-5 py-4">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{digest}</ReactMarkdown>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("page.session.handoff.noDigest")}</p>
        )}
      </div>
    </div>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

export function SessionDetailPage() {
  const { id = "" }      = useParams<{ id: string }>()
  const [searchParams]   = useSearchParams()
  const highlightTurnId  = searchParams.get("turn") ?? undefined
  const { t }            = useTranslation()
  const qc               = useQueryClient()
  const { data, isLoading, error } = useSession(id)
  const sess           = data?.session
  const [events, setEvents] = useState<LiveEvent[]>([])

  // Live refresh: when a stream event references this session, refetch its turns
  // + handoff (debounced) so the detail reflects real-time activity rather than a
  // snapshot frozen at the last ingest.
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const refresh = useCallback(() => {
    if (timerRef.current !== null) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => {
      void qc.invalidateQueries({ queryKey: ["session", id] })
      void qc.invalidateQueries({ queryKey: ["handoff", id] })
    }, 300)
  }, [qc, id])
  useEffect(() => () => { if (timerRef.current !== null) clearTimeout(timerRef.current) }, [])

  const onEvent = useCallback((e: LiveEvent) => {
    const matches =
      e.session_id === id ||
      (sess ? e.session_id === sess.id || (!!sess.external_id && e.external_session_id === sess.external_id) : false)
    if (matches) {
      setEvents((prev) => [e, ...prev].slice(0, 50))
      refresh()
    }
  }, [id, sess, refresh])
  const streamStatus = useStream(onEvent, { onResync: refresh })

  return (
    <div className="space-y-4">
      {/* back link */}
      <Link
        to="/sessions"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        ← {t("page.session.back")}
      </Link>

      {isLoading && (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      )}
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error.message ?? t("common.error")}
        </p>
      )}

      {data && (() => {
        const { session, turns, ambiguous_session_ids } = data

        return (
          <>
            {/* ambiguous id warning */}
            {ambiguous_session_ids && ambiguous_session_ids.length > 0 && (
              <div className="rounded-lg border border-yellow-300 bg-yellow-50 px-4 py-3 text-sm dark:border-yellow-800 dark:bg-yellow-950">
                <p className="font-medium">{t("page.session.ambiguous")}</p>
                <ul className="mt-1 space-y-0.5 list-disc pl-4">
                  {ambiguous_session_ids.map((aid) => (
                    <li key={aid}>
                      <Link
                        to={`/sessions/${aid}`}
                        className="font-mono text-xs text-blue-600 hover:underline dark:text-blue-400"
                      >
                        {aid}
                      </Link>
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {/* session header */}
            <div className="rounded-lg border border-border bg-card px-5 py-4 space-y-2">
              <div className="flex items-start justify-between gap-3 flex-wrap">
                <h1 className="text-xl font-semibold leading-snug">
                  {session.title ?? session.project_name ?? session.id}
                </h1>
                <div className="flex items-center gap-2">
                  <LiveDot status={streamStatus} />
                  <StatusBadge status={session.status} />
                </div>
              </div>

              <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground">
                <span>{session.provider}</span>
                {session.project_name && <span>{session.project_name}</span>}
                {session.started_at  && <span>{reltime(session.started_at)}</span>}
                <span>{t("common.turns")}: {session.turn_count}</span>
                <span>{t("common.tokens")}: {fmtTokens(session.tokens)}</span>
              </div>

              {/* subagent linkage */}
              {session.is_subagent && (
                <div className="flex flex-wrap items-center gap-3 pt-1 text-xs text-muted-foreground">
                  <span className="rounded bg-muted px-1.5 py-0.5 text-[11px]">
                    {t("page.session.subagent")}
                  </span>
                  {session.agent_type && (
                    <span>
                      {t("page.session.agentType")}: <strong>{session.agent_type}</strong>
                    </span>
                  )}
                  {session.parent_session_id && (
                    <Link
                      to={`/sessions/${session.parent_session_id}`}
                      className="text-blue-600 hover:underline dark:text-blue-400"
                    >
                      ↑ {t("page.session.parent")}
                    </Link>
                  )}
                </div>
              )}

              {/* subagent count for top-level orchestrators */}
              {!session.is_subagent && session.subagent_count != null && session.subagent_count > 0 && (
                <p className="text-xs text-muted-foreground">
                  {t("page.session.subagentCount", { count: session.subagent_count })}
                </p>
              )}
            </div>

            {/* recent live events for this session */}
            <RecentEvents
              events={events}
              viewAllHref={`/events?session=${encodeURIComponent(session.id)}`}
              max={5}
              showSession={false}
            />

            {/* tabs */}
            <Tabs.Root defaultValue="turns">
              <Tabs.List
                className="flex gap-1 rounded-lg bg-muted p-1 w-fit"
                aria-label={t("page.session.tabsLabel")}
              >
                {(["turns", "handoff"] as const).map((tab) => (
                  <Tabs.Tab
                    key={tab}
                    value={tab}
                    className="rounded-md px-4 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground aria-selected:bg-background aria-selected:text-foreground aria-selected:shadow-sm"
                  >
                    {t(`page.session.tabs.${tab}`)}
                  </Tabs.Tab>
                ))}
              </Tabs.List>

              <Tabs.Panel value="turns" className="pt-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                <TurnsTab turns={turns} highlightTurnId={highlightTurnId} />
              </Tabs.Panel>

              <Tabs.Panel value="handoff" className="pt-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                <HandoffTab sessionId={id} />
              </Tabs.Panel>
            </Tabs.Root>
          </>
        )
      })()}
    </div>
  )
}
