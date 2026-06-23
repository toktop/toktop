import { useState }                   from "react"
import { useParams, Link }             from "react-router-dom"
import { useTranslation }              from "react-i18next"
import { Tabs }                        from "@base-ui/react/tabs"
import ReactMarkdown                   from "react-markdown"
import remarkGfm                       from "remark-gfm"

import { useSession, useHandoff }      from "@/api/queries"
import type { AgentRun, Turn }         from "@/api/types"
import { StatusBadge }                 from "@/components/status-badge"
import { reltime, fmtTokens, fmtMs }   from "@/lib/format"

// ── helpers ───────────────────────────────────────────────────────────────────

function fmtBytes(n?: number): string {
  if (!n) return "—"
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)} MiB`
  if (n >= 1_024)     return `${(n / 1_024).toFixed(1)} KiB`
  return `${n} B`
}

// ── turn row ──────────────────────────────────────────────────────────────────

function TurnRow({ turn }: { turn: Turn }) {
  const { t }    = useTranslation()
  const [open, setOpen] = useState(false)

  const userText  = turn.user_message?.trim()    || ""
  const asstText  = turn.assistant_final?.trim() || ""
  const tokenStr  = fmtTokens(turn.tokens)
  const hasTools  = turn.tool_call_count > 0

  return (
    <div className="rounded-lg border border-border bg-card overflow-hidden">
      {/* header row */}
      <div className="flex items-start justify-between gap-4 px-4 py-3">
        <div className="min-w-0 flex-1 space-y-1.5">
          {/* index + status */}
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-xs font-mono text-muted-foreground">
              {t("page.session.turns.index", { n: turn.index + 1 })}
            </span>
            <StatusBadge status={turn.status} />
            {tokenStr !== "0" && (
              <span className="text-xs text-muted-foreground">
                {t("page.session.turns.tokens", { n: tokenStr })}
              </span>
            )}
            {hasTools && (
              <span className="text-xs text-muted-foreground">
                {t("page.session.turns.tools", { n: turn.tool_call_count })}
              </span>
            )}
          </div>

          {/* user message */}
          {userText && (
            <p className="text-sm leading-snug line-clamp-2 text-foreground/80">
              <span className="mr-1 text-xs uppercase tracking-wide text-muted-foreground">
                {t("page.session.turns.user")}
              </span>
              {userText}
            </p>
          )}

          {/* assistant message */}
          {asstText && (
            <p className="text-sm leading-snug line-clamp-2 text-foreground">
              <span className="mr-1 text-xs uppercase tracking-wide text-muted-foreground">
                {t("page.session.turns.assistant")}
              </span>
              {asstText}
            </p>
          )}
        </div>

        {/* expand toggle */}
        {hasTools && (
          <button
            type="button"
            onClick={() => setOpen(v => !v)}
            className="shrink-0 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted"
          >
            {open
              ? t("page.session.turns.hideTools")
              : t("page.session.turns.showTools")}
          </button>
        )}
      </div>

      {/* tool calls expansion */}
      {open && turn.tool_calls && turn.tool_calls.length > 0 && (
        <div className="border-t border-border divide-y divide-border bg-muted/30">
          {turn.tool_calls.map((tc) => (
            <div key={tc.id} className="px-4 py-2 text-xs">
              <div className="flex items-center gap-2 flex-wrap">
                <span className="font-mono font-medium text-foreground">{tc.name}</span>
                <StatusBadge status={tc.status} />
                {tc.duration_ms != null && (
                  <span className="text-muted-foreground">{fmtMs(tc.duration_ms)}</span>
                )}
              </div>
              {tc.input && (
                <pre className="mt-1 max-h-24 overflow-auto rounded bg-background p-2 text-[11px] text-muted-foreground whitespace-pre-wrap break-all">
                  {tc.input}
                </pre>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
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

function TurnsTab({ turns }: { turns: Turn[] }) {
  const { t } = useTranslation()
  if (turns.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("page.session.turns.empty")}</p>
  }
  return (
    <div className="space-y-3">
      {turns.map((turn) => <TurnRow key={turn.id} turn={turn} />)}
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
  const { id = "" }    = useParams<{ id: string }>()
  const { t }          = useTranslation()
  const { data, isLoading, error } = useSession(id)

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
                <StatusBadge status={session.status} />
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
                <TurnsTab turns={turns} />
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
