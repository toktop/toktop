import { useTranslation }  from "react-i18next"

import { reltime }         from "@/lib/format"
import { useConfig, useDaemon, useSources } from "@/api/queries"
import { StatusBadge }                       from "@/components/status-badge"
import type {
  ConfigResponse,
  DaemonBackpressure,
  DaemonCounters,
  DaemonStatus,
  SourceRoot,
} from "@/api/types"

// ── helpers ───────────────────────────────────────────────────────────────────

function n(v: number | undefined): string {
  if (v == null) return "—"
  return v.toLocaleString()
}

// ── shared card ───────────────────────────────────────────────────────────────

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-card">
      <h2 className="border-b border-border px-4 py-2.5 text-sm font-semibold">{title}</h2>
      <div className="px-4 py-3">{children}</div>
    </section>
  )
}

// kv row for simple key→value display
function KV({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 py-1.5 border-b border-border/50 last:border-0">
      <dt className="text-sm text-muted-foreground shrink-0">{label}</dt>
      <dd className="text-sm text-right font-mono break-all">{children}</dd>
    </div>
  )
}

// ── runtime status card ───────────────────────────────────────────────────────

function RuntimeCard({ d }: { d: DaemonStatus }) {
  const { t } = useTranslation()
  return (
    <Card title={t("page.daemon.runtime.title")}>
      <dl>
        <KV label={t("page.daemon.runtime.state")}>
          <StatusBadge status={d.state} />
        </KV>
        <KV label={t("page.daemon.runtime.sources")}>
          {d.sources.length > 0 ? d.sources.join(", ") : "—"}
        </KV>
        <KV label={t("page.daemon.runtime.interval")}>{d.interval}</KV>
        <KV label={t("page.daemon.runtime.debounce")}>{d.debounce}</KV>
        <KV label={t("page.daemon.runtime.startedAt")}>{reltime(d.started_at)}</KV>
        <KV label={t("page.daemon.runtime.lastFull")}>
          {d.last_full_at ? `${reltime(d.last_full_at)}${d.last_full_reason ? ` (${d.last_full_reason})` : ""}` : "—"}
        </KV>
        <KV label={t("page.daemon.runtime.lastFile")}>
          {d.last_file_at ? reltime(d.last_file_at) : "—"}
        </KV>
        {d.last_file_path && (
          <KV label={t("page.daemon.runtime.lastFilePath")}>
            <span className="text-xs text-muted-foreground">{d.last_file_path}</span>
          </KV>
        )}
        <KV label={t("page.daemon.runtime.pendingFiles")}>{n(d.pending_files)}</KV>
        <KV label={t("page.daemon.runtime.watchedPaths")}>{n(d.watched_paths)}</KV>
      </dl>
    </Card>
  )
}

// ── counters card ─────────────────────────────────────────────────────────────

function CountersCard({ c }: { c: DaemonCounters }) {
  const { t } = useTranslation()
  const stats: [string, number | undefined][] = [
    [t("page.daemon.counters.fullRuns"),     c.full_runs],
    [t("page.daemon.counters.fullFailures"), c.full_failures],
    [t("page.daemon.counters.fileRuns"),     c.file_runs],
    [t("page.daemon.counters.fileFailures"), c.file_failures],
    [t("page.daemon.counters.unmapped"),     c.unmapped_files],
    [t("page.daemon.counters.ingestDrop"),   c.ingest_auto_dropped_total],
    [t("page.daemon.counters.emitDrop"),     c.emit_dropped_total],
  ]

  return (
    <Card title={t("page.daemon.counters.title")}>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        {stats.map(([label, value]) => (
          <div key={label} className="rounded-md border border-border/60 bg-muted/30 px-3 py-2">
            <p className="text-xl font-semibold tabular-nums">{n(value)}</p>
            <p className="text-[11px] text-muted-foreground">{label}</p>
          </div>
        ))}
      </div>
    </Card>
  )
}

// ── backpressure card ─────────────────────────────────────────────────────────

function BackpressureCard({ bp }: { bp: DaemonBackpressure }) {
  const { t } = useTranslation()
  return (
    <Card title={t("page.daemon.backpressure.title")}>
      <dl>
        <KV label={t("page.daemon.backpressure.liveSessions")}>{n(bp.live_sessions)}</KV>
        <KV label={t("page.daemon.backpressure.persistQueueLen")}>{n(bp.persist_queue_len)}</KV>
        <KV label={t("page.daemon.backpressure.durableLag")}>{n(bp.durable_lag)}</KV>
        <KV label={t("page.daemon.backpressure.persistFull")}>{n(bp.persist_queue_full_total)}</KV>
        <KV label={t("page.daemon.backpressure.sseDropped")}>{n(bp.sse_slow_subscriber_dropped_total)}</KV>
        <KV label={t("page.daemon.backpressure.spoolDropped")}>{n(bp.spool_dropped_total)}</KV>
        <KV label={t("page.daemon.backpressure.spoolDroppedBytes")}>{n(bp.spool_dropped_bytes)}</KV>
      </dl>
    </Card>
  )
}

// ── config card ───────────────────────────────────────────────────────────────

function ConfigCard({ cfg }: { cfg: ConfigResponse }) {
  const { t } = useTranslation()
  return (
    <Card title={t("page.daemon.config.title")}>
      <dl>
        <KV label={t("page.daemon.config.homeDir")}>{cfg.home_dir}</KV>
        <KV label={t("page.daemon.config.configDir")}>{cfg.config_dir}</KV>
        <KV label={t("page.daemon.config.dataDir")}>{cfg.data_dir}</KV>
        <KV label={t("page.daemon.config.apiToken")}>
          {cfg.api_token_set
            ? <span className="text-green-600 dark:text-green-400">{t("page.daemon.config.tokenSet")}</span>
            : <span className="text-muted-foreground">{t("page.daemon.config.tokenUnset")}</span>}
        </KV>
        <KV label={t("page.daemon.config.redact")}>{cfg.redact}</KV>
      </dl>

      {/* roots per provider */}
      {Object.keys(cfg.roots).length > 0 && (
        <div className="mt-3 space-y-2">
          <p className="text-xs font-medium text-muted-foreground">{t("page.daemon.config.roots")}</p>
          {Object.entries(cfg.roots).map(([provider, paths]) => (
            <div key={provider}>
              <p className="text-xs uppercase tracking-wide text-muted-foreground mb-0.5">{provider}</p>
              <ul className="space-y-0.5">
                {(paths ?? []).map((p) => (
                  <li key={p} className="font-mono text-xs text-foreground/80 break-all">{p}</li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

// ── sources card ──────────────────────────────────────────────────────────────

function SourcesCard({ sources }: { sources: SourceRoot[] }) {
  const { t } = useTranslation()
  return (
    <Card title={t("page.daemon.sources.title")}>
      {sources.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("page.daemon.sources.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-border text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="pb-1.5 text-left font-medium">{t("page.daemon.sources.source")}</th>
                <th scope="col" className="pb-1.5 text-left font-medium">{t("page.daemon.sources.root")}</th>
                <th scope="col" className="pb-1.5 text-left font-medium">{t("page.daemon.sources.exists")}</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((s, i) => (
                <tr key={`${s.source}-${s.root}-${i}`}
                    className="border-b border-border/50 last:border-0">
                  <td className="py-1.5 pr-4 text-xs uppercase tracking-wide text-muted-foreground">{s.source}</td>
                  <td className="py-1.5 pr-4 font-mono text-xs break-all">{s.root || "—"}</td>
                  <td className="py-1.5">
                    {s.root === "" ? (
                      <span className="text-muted-foreground text-xs">—</span>
                    ) : s.exists ? (
                      <span className="text-green-600 dark:text-green-400 text-xs">{t("page.daemon.sources.yes")}</span>
                    ) : (
                      <span className="text-destructive text-xs">{t("page.daemon.sources.no")}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

export function DaemonPage() {
  const { t }      = useTranslation()
  const daemon     = useDaemon()
  const config     = useConfig()
  const sources    = useSources()

  const daemonErr = daemon.error as Error | null
  const cfgErr    = config.error as Error | null
  const srcErr    = sources.error as Error | null

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.daemon.title")}</h1>

      {/* daemon status */}
      {daemon.isLoading && (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      )}
      {daemonErr && (
        <p className="text-sm text-destructive" role="alert">
          {daemonErr.message ?? t("common.error")}
        </p>
      )}
      {daemon.data && (
        <div className="grid gap-4 md:grid-cols-2">
          <RuntimeCard     d={daemon.data} />
          <CountersCard    c={daemon.data.counters} />
          <BackpressureCard bp={daemon.data.backpressure} />
        </div>
      )}

      {/* config */}
      {cfgErr && (
        <p className="text-sm text-destructive" role="alert">
          {cfgErr.message ?? t("common.error")}
        </p>
      )}
      {config.data && <ConfigCard cfg={config.data} />}

      {/* sources */}
      {srcErr && (
        <p className="text-sm text-destructive" role="alert">
          {srcErr.message ?? t("common.error")}
        </p>
      )}
      {sources.data && <SourcesCard sources={sources.data} />}
    </div>
  )
}
