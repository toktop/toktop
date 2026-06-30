import { useMemo, useRef, useState } from "react"
import type { ReactNode }   from "react"
import { useTranslation }   from "react-i18next"
import { Link }             from "react-router-dom"
import { Tabs }             from "@base-ui/react/tabs"
import { useVirtualizer }   from "@tanstack/react-virtual"
import { parseISO } from "date-fns"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { reltime, fmtMs, fmtNum, topN } from "@/lib/format"
import { DataTable }       from "@/components/data-table"
import type { Column }     from "@/components/data-table"
import { StatusBadge }     from "@/components/status-badge"
import { BarChartCard }    from "@/components/bar-chart-card"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import type { ChartConfig } from "@/components/ui/chart"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  useActivity,
  useMcps,
  useModels,
  useProjects,
  useSkills,
  useToolCalls,
  useTools,
  useUnusedMcps,
  useUnusedSkills,
} from "@/api/queries"
import type {
  ActivityBucket,
  MCPListItem,
  ModelListItem,
  ProjectListItem,
  SkillListItem,
  ToolCallListItem,
  ToolListItem,
} from "@/api/types"

// Time-window filter shared by the insights charts and every tab's listing, so
// the range chips scope the whole page at once. Empty range → undefined → no
// since param → all-time (the default).
type AFilter = Record<string, string> | undefined

// ── helpers ───────────────────────────────────────────────────────────────────

// local alias for the shared integer formatter (terse cell rendering)
const n = fmtNum

// A failed/rejected count cell: a clickable button that opens the drill-down when
// the count is non-zero, a muted "0" otherwise.
function countCell(count: number, colorClass: string, onOpen: () => void): ReactNode {
  if (count === 0) return <span className="text-muted-foreground">0</span>
  return (
    <button
      type="button"
      onClick={onOpen}
      className={`rounded-sm underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none ${colorClass}`}
    >
      {n(count)}
    </button>
  )
}

// ── tab state helpers ─────────────────────────────────────────────────────────

// tabStatus renders a tab's loading / error / empty placeholder straight from its
// react-query result, or null when there are rows to show — so each tab drops the
// repeated {loading,error} literal and the per-tab `error as Error` cast.
function tabStatus<T>(
  q: { isLoading: boolean; error: unknown; data: T[] | undefined },
  emptyLabel: string,
  t: (key: string) => string,
): ReactNode | null {
  if (q.isLoading)     return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
  if (q.error)         return <p className="text-sm text-destructive" role="alert">{(q.error as Error).message ?? t("common.error")}</p>
  if (!q.data?.length) return <p className="text-sm text-muted-foreground">{emptyLabel}</p>
  return null
}

// A checkbox toggle reused by the mcps/skills tabs (switches the data source).
function UnusedToggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm select-none">
      <input
        type="checkbox"
        className="h-4 w-4 rounded border-input accent-primary"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label}
    </label>
  )
}

// ── insights (time-windowed charts above the tabs) ────────────────────────────

const RANGES = ["all", "30m", "1h", "1d", "1w"] as const
type Range = (typeof RANGES)[number]

// chips → trend bucket: the API width string and the matching step in ms (used to
// zero-fill empty buckets so the category x-axis doesn't collapse idle gaps). One
// table keeps the two in lockstep. Widths kept to ~20-40 points across the window.
const RANGE_BUCKET: Record<Range, { api: string; ms: number }> = {
  "30m": { api: "1m",  ms: 60_000 },
  "1h":  { api: "2m",  ms: 120_000 },
  "1d":  { api: "1h",  ms: 3_600_000 },
  "1w":  { api: "6h",  ms: 21_600_000 },
  "all": { api: "24h", ms: 86_400_000 },
}
function rangeBucket(r: Range): string {
  return RANGE_BUCKET[r].api
}
// Bucket starts are UTC instants floored to clean UTC boundaries (see
// ActivitySeries); render the axis/tooltip in UTC too. date-fns format() would
// shift them into the browser's local zone, so the ticks would no longer sit on
// the actual bucket edges for non-UTC users.
function pad2(n: number): string {
  return String(n).padStart(2, "0")
}
function utcHHmm(d: Date): string {
  return `${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`
}
function utcMMdd(d: Date): string {
  return `${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`
}
// bucket-axis label: within a day show the clock, across days show the date.
function bucketTick(iso: string, r: Range): string {
  const d = parseISO(iso)
  return r === "30m" || r === "1h" || r === "1d" ? utcHHmm(d) : utcMMdd(d)
}
// compact y-axis label so a million-scale tokens series and a tens-scale turns
// series both fit the narrow axis (1_500_000 → "1.5M", 7_000 → "7K").
const compactNum = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 })

function RangeChips({ range, onChange }: { range: Range; onChange: (r: Range) => void }) {
  const { t } = useTranslation()
  return (
    <div className="flex w-fit gap-1 rounded-lg border border-border bg-muted/40 p-1">
      {RANGES.map((r) => (
        <button
          key={r}
          type="button"
          onClick={() => onChange(r)}
          aria-pressed={range === r}
          className="rounded-md px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm"
        >
          {t(`page.analytics.insights.range.${r}`)}
        </button>
      ))}
    </div>
  )
}

const TREND_METRICS = ["turns", "tools", "tokens", "sessions"] as const
type TrendMetric = (typeof TREND_METRICS)[number]
type TrendPoint = { bucket: string; turns: number; tools: number; tokens: number; sessions: number }

// The activity area chart: one selectable metric over time. isAnimationActive is
// off for the same recharts-3.8/React-19 reason as the bar charts (the enter
// animation never fires, leaving an empty shape).
function ActivityTrend({ data, range, loading }: { data: ActivityBucket[]; range: Range; loading: boolean }) {
  const { t }               = useTranslation()
  const [metric, setMetric] = useState<TrendMetric>("turns")
  // ActivitySeries omits empty buckets; fill them with zeros here so the area
  // chart spaces points by real time, not by index — an idle gap reads as a flat
  // zero stretch instead of a straight slope drawn across it. Keyed by epoch ms
  // (the server's RFC3339 "…Z" and JS's "….000Z" differ as strings). Capped so a
  // sparse all-time span can't explode the series.
  const points = useMemo<TrendPoint[]>(() => {
    if (data.length === 0) return []
    const step  = RANGE_BUCKET[range].ms
    const byT   = new Map(data.map((b) => [parseISO(b.bucket).getTime(), b]))
    const first = parseISO(data[0].bucket).getTime()
    const last  = parseISO(data[data.length - 1].bucket).getTime()
    const out: TrendPoint[] = []
    for (let t = first; t <= last && out.length < 2000; t += step) {
      const b = byT.get(t)
      out.push(b
        ? { bucket: b.bucket, turns: b.turns, tools: b.tool_calls, tokens: b.input_tokens + b.output_tokens, sessions: b.sessions }
        : { bucket: new Date(t).toISOString(), turns: 0, tools: 0, tokens: 0, sessions: 0 })
    }
    return out
  }, [data, range])
  const config = useMemo<ChartConfig>(
    () => ({ [metric]: { label: t(`page.analytics.insights.metric.${metric}`), color: "var(--chart-1)" } }),
    [metric, t],
  )

  return (
    <Card size="sm">
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="text-sm">{t("page.analytics.insights.trend")}</CardTitle>
        <div className="flex gap-1">
          {TREND_METRICS.map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMetric(m)}
              aria-pressed={metric === m}
              className="rounded-md px-2 py-0.5 text-xs text-muted-foreground transition-colors hover:text-foreground aria-pressed:bg-muted aria-pressed:text-foreground"
            >
              {t(`page.analytics.insights.metric.${m}`)}
            </button>
          ))}
        </div>
      </CardHeader>
      <CardContent>
        {!loading && points.length === 0 ? (
          <p className="py-12 text-center text-sm text-muted-foreground">{t("page.analytics.insights.empty")}</p>
        ) : (
          <ChartContainer config={config} className="aspect-auto h-[200px] w-full">
            <AreaChart data={points} margin={{ left: 4, right: 12, top: 8, bottom: 0 }}>
              <CartesianGrid vertical={false} strokeDasharray="3 3" />
              <XAxis
                dataKey="bucket"
                tickFormatter={(v: string) => bucketTick(v, range)}
                tickLine={false}
                axisLine={false}
                tick={{ fontSize: 11 }}
                minTickGap={24}
              />
              <YAxis tickLine={false} axisLine={false} tick={{ fontSize: 11 }} width={44} tickFormatter={(v: number) => compactNum.format(v)} />
              <ChartTooltip
                cursor={false}
                content={<ChartTooltipContent labelFormatter={(v) => (typeof v === "string" ? `${utcMMdd(parseISO(v))} ${utcHHmm(parseISO(v))}` : "")} />}
              />
              <Area
                dataKey={metric}
                type="monotone"
                stroke={`var(--color-${metric})`}
                fill={`var(--color-${metric})`}
                fillOpacity={0.15}
                strokeWidth={2}
                isAnimationActive={false}
              />
            </AreaChart>
          </ChartContainer>
        )}
      </CardContent>
    </Card>
  )
}

function InsightsSection({ range, filter }: { range: Range; filter: AFilter }) {
  const { t }    = useTranslation()
  const activity = useActivity({ ...(filter ?? {}), bucket: rangeBucket(range) })
  const projects = useProjects(filter)
  const tools    = useTools(filter)

  const topProjects = useMemo(() => topN(projects.data, (p) => p.turn_count, (p) => p.name), [projects.data])
  const topTools    = useMemo(() => topN(tools.data, (tc) => tc.call_count, (tc) => tc.name), [tools.data])

  return (
    <section className="space-y-3" aria-label={t("page.analytics.insights.trend")}>
      <ActivityTrend data={activity.data ?? []} range={range} loading={activity.isLoading} />
      <div className="grid gap-4 lg:grid-cols-2">
        <BarChartCard
          title={t("page.analytics.insights.topProjects")}
          metricLabel={t("page.analytics.insights.metric.turns")}
          data={topProjects}
          emptyText={t("page.analytics.insights.empty")}
        />
        <BarChartCard
          title={t("page.analytics.insights.topTools")}
          metricLabel={t("page.analytics.insights.metric.tools")}
          data={topTools}
          emptyText={t("page.analytics.insights.empty")}
        />
      </div>
    </section>
  )
}

// ── projects tab ──────────────────────────────────────────────────────────────

function ProjectsTab({ filter }: { filter: AFilter }) {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useProjects(filter)
  const items: ProjectListItem[]   = data ?? []

  const status = tabStatus({ isLoading, error, data }, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ProjectListItem>[] = [
    {
      header: t("page.analytics.projects.name"), width: "w-[30%]",
      cell: (p) => (
        <span>
          <span className="font-medium">{p.name}</span>
          {p.path && <span className="ml-2 font-mono text-xs text-muted-foreground">{" "}{p.path}</span>}
        </span>
      ),
    },
    {
      header: t("page.analytics.projects.source"), width: "w-[12%]",
      cell: (p) => <span className="text-xs uppercase tracking-wide text-muted-foreground">{p.source_id}</span>,
    },
    { header: t("page.analytics.projects.sessions"), width: "w-[13%]", right: true, cell: (p) => n(p.session_count) },
    { header: t("page.analytics.projects.turns"),    width: "w-[13%]", right: true, cell: (p) => n(p.turn_count) },
    { header: t("page.analytics.projects.tools"),    width: "w-[13%]", right: true, cell: (p) => n(p.tool_call_count) },
    {
      header: t("page.analytics.projects.lastActivity"), width: "w-[19%]", right: true,
      cell: (p) => <span className="text-xs text-muted-foreground">{reltime(p.last_activity)}</span>,
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={items}
      rowKey={(p) => p.id}
      filterText={(p) => `${p.name} ${p.path ?? ""} ${p.source_id}`}
    />
  )
}

// ── tools tab ─────────────────────────────────────────────────────────────────

type ToolDrill = { tool: ToolListItem; status: "failed" | "rejected" }

function ToolsTab({ filter }: { filter: AFilter }) {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useTools(filter)
  const items: ToolListItem[]      = data ?? []
  const [drill, setDrill]          = useState<ToolDrill | null>(null)

  const status = tabStatus({ isLoading, error, data }, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ToolListItem>[] = [
    {
      header: t("page.analytics.tools.name"), width: "w-[18%]",
      cell: (tc) => <span className="font-mono font-medium">{tc.name}</span>,
    },
    {
      header: t("page.analytics.tools.kind"), width: "w-[11%]",
      cell: (tc) => <span className="text-xs text-muted-foreground">{tc.kind}</span>,
    },
    {
      header: t("page.analytics.tools.mcpServer"), width: "w-[19%]",
      cell: (tc) => <span className="text-xs text-muted-foreground">{tc.mcp_server ?? "—"}</span>,
    },
    { header: t("page.analytics.tools.calls"), width: "w-[10%]", right: true, cell: (tc) => n(tc.call_count) },
    { header: t("page.analytics.tools.turns"), width: "w-[10%]", right: true, cell: (tc) => n(tc.turn_count) },
    {
      header: t("page.analytics.tools.failed"), width: "w-[10%]", right: true,
      cell: (tc) => countCell(tc.failed_count, "text-destructive", () => setDrill({ tool: tc, status: "failed" })),
    },
    {
      header: t("page.analytics.tools.rejected"), width: "w-[10%]", right: true,
      cell: (tc) => countCell(tc.rejected_count, "text-yellow-600 dark:text-yellow-400", () => setDrill({ tool: tc, status: "rejected" })),
    },
    {
      header: t("page.analytics.tools.lastUsed"), width: "w-[12%]", right: true,
      cell: (tc) => <span className="text-xs text-muted-foreground">{reltime(tc.last_used_at)}</span>,
    },
  ]

  return (
    <>
      <DataTable
        columns={columns}
        rows={items}
        rowKey={(tc, i) => `${tc.kind}-${tc.name}-${tc.mcp_server ?? ""}-${i}`}
        filterText={(tc) => `${tc.name} ${tc.kind} ${tc.mcp_server ?? ""}`}
      />
      <ToolCallsDialog drill={drill} filter={filter} onClose={() => setDrill(null)} />
    </>
  )
}

// ── tool-call drill-down ────────────────────────────────────────────────────────

// One failed/rejected call: where it happened (session link + project + when) and
// the failure detail. The detail prefers `error`, falling back to `output` — most
// tools (e.g. Bash) leave `error` empty and carry the failure text in `output`.
function ToolCallEntry({ call }: { call: ToolCallListItem }) {
  const { t }  = useTranslation()
  const detail = call.error || call.output || ""
  const where  = call.session_title || call.session_id

  return (
    <div className="rounded-lg border border-border bg-muted/20 p-3 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={call.status} />
        <Link
          to={`/sessions/${call.session_id}?turn=${encodeURIComponent(call.turn_id)}`}
          className="font-medium text-foreground hover:underline"
          title={t("page.analytics.drilldown.openTurn")}
        >
          {where}
          <span className="ms-1.5 font-normal text-muted-foreground">{t("page.analytics.drilldown.turn", { n: call.turn_index + 1 })}</span>
        </Link>
        {call.project_name && <span className="text-muted-foreground">{call.project_name}</span>}
        <span className="uppercase tracking-wide text-muted-foreground">{call.source_id}</span>
        <span className="ms-auto text-muted-foreground">{reltime(call.started_at)}</span>
        {call.duration_ms != null && <span className="text-muted-foreground">{fmtMs(call.duration_ms)}</span>}
      </div>

      {call.input && (
        <div className="mt-2">
          <div className="mb-1 font-medium uppercase tracking-wide text-muted-foreground">{t("page.analytics.drilldown.input")}</div>
          <pre className="max-h-24 overflow-auto rounded bg-background p-2 text-[11px] break-all whitespace-pre-wrap text-muted-foreground">{call.input}</pre>
        </div>
      )}

      <div className="mt-2">
        <div className="mb-1 font-medium uppercase tracking-wide text-muted-foreground">{t("page.analytics.drilldown.detail")}</div>
        {detail
          ? <pre className="max-h-48 overflow-auto rounded bg-background p-2 text-[11px] break-all whitespace-pre-wrap text-foreground">{detail}</pre>
          : <p className="text-muted-foreground italic">{t("page.analytics.drilldown.noDetail")}</p>}
      </div>
    </div>
  )
}

function ToolCallsDialog({ drill, filter, onClose }: { drill: ToolDrill | null; filter: AFilter; onClose: () => void }) {
  const { t }      = useTranslation()
  const tool       = drill?.tool ?? null
  const kindStatus = drill?.status ?? "failed"
  const total      = tool ? (kindStatus === "failed" ? tool.failed_count : tool.rejected_count) : 0

  // Scope the drill-down to the page's active range window (filter.since/until)
  // so the listed calls match the windowed failed/rejected count it was opened
  // from — otherwise the dialog shows all-time calls against a windowed total.
  const { data, isLoading, error } = useToolCalls(
    { name: tool?.name ?? "", kind: tool?.kind, mcp_server: tool?.mcp_server, status: kindStatus,
      since: filter?.since, until: filter?.until },
    drill !== null,
  )
  const calls: ToolCallListItem[] = data ?? []
  const scrollRef = useRef<HTMLDivElement>(null)

  // The drill-down can return up to DefaultToolCallLimit (500) variable-height
  // cards; virtualize so only the visible window is in the DOM. measureElement
  // measures each card's real height (input + output/error vary).
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count:            calls.length,
    getScrollElement: () => scrollRef.current,
    estimateSize:     () => 160,
    overscan:         6,
  })

  return (
    <Dialog open={drill !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent>
        <div className="flex flex-col gap-1">
          <DialogTitle>
            <span className="font-mono">{tool?.name}</span>
            {" · "}
            {t(`page.analytics.tools.${kindStatus}`)}
          </DialogTitle>
          {!isLoading && !error && calls.length > 0 && (
            <DialogDescription>
              {calls.length >= total
                ? t("page.analytics.drilldown.showingAll", { total })
                : t("page.analytics.drilldown.showing", { shown: calls.length, total })}
            </DialogDescription>
          )}
        </div>

        <div ref={scrollRef} className="-mx-1 flex-1 overflow-y-auto px-1">
          {isLoading ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("common.loading")}</p>
          ) : error ? (
            <p className="py-8 text-center text-sm text-destructive" role="alert">{(error as Error).message || t("common.error")}</p>
          ) : calls.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("page.analytics.drilldown.empty")}</p>
          ) : (
            <div style={{ position: "relative", width: "100%", height: virtualizer.getTotalSize() }}>
              {virtualizer.getVirtualItems().map((vi) => (
                <div
                  key={calls[vi.index].id}
                  data-index={vi.index}
                  ref={virtualizer.measureElement}
                  className="pb-3"
                  style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${vi.start}px)` }}
                >
                  <ToolCallEntry call={calls[vi.index]} />
                </div>
              ))}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}

// ── models tab ────────────────────────────────────────────────────────────────

function ModelsTab({ filter }: { filter: AFilter }) {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useModels(filter)
  const items: ModelListItem[]     = data ?? []

  const status = tabStatus({ isLoading, error, data }, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ModelListItem>[] = [
    {
      header: t("page.analytics.models.model"), width: "w-[16%]",
      cell: (m) => <span className="font-mono font-medium">{m.model || "—"}</span>,
    },
    {
      header: t("page.analytics.models.provider"), width: "w-[10%]",
      cell: (m) => <span className="text-xs uppercase tracking-wide text-muted-foreground">{m.provider || "—"}</span>,
    },
    { header: t("page.analytics.models.calls"),          width: "w-[8%]",  right: true, cell: (m) => n(m.call_count) },
    { header: t("page.analytics.models.turns"),          width: "w-[8%]",  right: true, cell: (m) => n(m.turn_count) },
    { header: t("page.analytics.models.inputTokens"),    width: "w-[9%]",  right: true, cell: (m) => n(m.input_tokens) },
    { header: t("page.analytics.models.outputTokens"),   width: "w-[9%]",  right: true, cell: (m) => n(m.output_tokens) },
    { header: t("page.analytics.models.cacheRead"),      width: "w-[9%]",  right: true, cell: (m) => n(m.cache_read_tokens) },
    { header: t("page.analytics.models.cacheWrite"),     width: "w-[9%]",  right: true, cell: (m) => n(m.cache_write_tokens) },
    { header: t("page.analytics.models.cacheWriteLong"), width: "w-[10%]", right: true, cell: (m) => n(m.cache_write_long_tokens) },
    {
      header: t("page.analytics.models.lastUsed"), width: "w-[12%]", right: true,
      cell: (m) => <span className="text-xs text-muted-foreground">{reltime(m.last_used_at)}</span>,
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={items}
      minWidth="min-w-[1100px]"
      rowKey={(m, i) => `${m.provider}-${m.model}-${i}`}
      filterText={(m) => `${m.model} ${m.provider}`}
    />
  )
}

// ── mcps tab ──────────────────────────────────────────────────────────────────

function MCPsTab({ filter }: { filter: AFilter }) {
  const { t }                       = useTranslation()
  const [unusedOnly, setUnusedOnly] = useState(false)
  const all                         = useMcps(filter)
  const unused                      = useUnusedMcps()
  const active                      = unusedOnly ? unused : all
  const items: MCPListItem[]        = active.data ?? []

  const toggle = <UnusedToggle label={t("page.analytics.mcps.unusedOnly")} checked={unusedOnly} onChange={setUnusedOnly} />

  const status = tabStatus(active, t("page.analytics.empty"), t)
  if (status) {
    return (
      <div className="space-y-3">
        <div className="flex items-center">{toggle}</div>
        {status}
      </div>
    )
  }

  const columns: Column<MCPListItem>[] = [
    {
      header: t("page.analytics.mcps.server"), width: "w-[18%]",
      cell: (m) => <span className="font-mono font-medium">{m.server || "—"}</span>,
    },
    {
      header: t("page.analytics.mcps.source"), width: "w-[12%]",
      cell: (m) => <span className="text-xs uppercase tracking-wide text-muted-foreground">{m.source_id}</span>,
    },
    {
      header: t("page.analytics.mcps.declared"), width: "w-[9%]",
      cell: (m) => (
        <span className={`text-xs ${m.declared ? "text-green-600 dark:text-green-400" : "text-muted-foreground"}`}>
          {m.declared ? t("page.analytics.mcps.yes") : t("page.analytics.mcps.no")}
        </span>
      ),
    },
    {
      header: t("page.analytics.mcps.scope"), width: "w-[12%]",
      cell: (m) => <span className="text-xs text-muted-foreground">{m.scope ?? "—"}</span>,
    },
    { header: t("page.analytics.mcps.calls"),        width: "w-[9%]",  right: true, cell: (m) => n(m.call_count) },
    { header: t("page.analytics.mcps.tools"),        width: "w-[9%]",  right: true, cell: (m) => n(m.tool_count) },
    { header: t("page.analytics.mcps.turns"),        width: "w-[9%]",  right: true, cell: (m) => n(m.turn_count) },
    { header: t("page.analytics.mcps.availability"), width: "w-[9%]",  right: true, cell: (m) => n(m.availability_observed) },
    {
      header: t("page.analytics.mcps.lastUsed"), width: "w-[13%]", right: true,
      cell: (m) => <span className="text-xs text-muted-foreground">{reltime(m.last_used_at)}</span>,
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={items}
      toolbarExtra={toggle}
      rowKey={(m, i) => `${m.source_id}-${m.server}-${i}`}
      filterText={(m) => `${m.server} ${m.source_id} ${m.scope ?? ""}`}
    />
  )
}

// ── skills tab ────────────────────────────────────────────────────────────────

function SkillsTab({ filter }: { filter: AFilter }) {
  const { t }                       = useTranslation()
  const [unusedOnly, setUnusedOnly] = useState(false)
  const all                         = useSkills(filter)
  const unused                      = useUnusedSkills()
  const active                      = unusedOnly ? unused : all
  const items: SkillListItem[]      = active.data ?? []

  const toggle = <UnusedToggle label={t("page.analytics.skills.unusedOnly")} checked={unusedOnly} onChange={setUnusedOnly} />

  const status = tabStatus(active, t("page.analytics.empty"), t)
  if (status) {
    return (
      <div className="space-y-3">
        <div className="flex items-center">{toggle}</div>
        {status}
      </div>
    )
  }

  const columns: Column<SkillListItem>[] = [
    {
      header: t("page.analytics.skills.name"), width: "w-[18%]",
      cell: (s) => (
        <span>
          <span className="font-mono font-medium">{s.name}</span>
          {s.version && <span className="ml-1 text-[10px] text-muted-foreground">{" "}v{s.version}</span>}
        </span>
      ),
    },
    {
      header: t("page.analytics.skills.source"), width: "w-[11%]",
      cell: (s) => <span className="text-xs uppercase tracking-wide text-muted-foreground">{s.source_id}</span>,
    },
    {
      header: t("page.analytics.skills.scope"), width: "w-[11%]",
      cell: (s) => <span className="text-xs text-muted-foreground">{s.scope ?? "—"}</span>,
    },
    {
      header: t("page.analytics.skills.installed"), width: "w-[10%]",
      cell: (s) => (
        <span className={`text-xs ${s.installed ? "text-green-600 dark:text-green-400" : "text-muted-foreground"}`}>
          {s.installed ? t("page.analytics.skills.yes") : t("page.analytics.skills.no")}
        </span>
      ),
    },
    {
      header: t("page.analytics.skills.description"), width: "w-[24%]",
      cell: (s) => <span className="text-xs text-muted-foreground">{s.description || "—"}</span>,
    },
    { header: t("page.analytics.skills.usedCount"), width: "w-[12%]", right: true, cell: (s) => n(s.inferred_used_count) },
    {
      header: t("page.analytics.skills.lastUsed"), width: "w-[14%]", right: true,
      cell: (s) => <span className="text-xs text-muted-foreground">{reltime(s.last_used_at)}</span>,
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={items}
      toolbarExtra={toggle}
      rowKey={(s, i) => `${s.source_id}-${s.name}-${i}`}
      filterText={(s) => `${s.name} ${s.source_id} ${s.scope ?? ""} ${s.description ?? ""}`}
    />
  )
}

// ── tab ids ───────────────────────────────────────────────────────────────────

type TabId = "projects" | "tools" | "models" | "mcps" | "skills"
const TABS: TabId[] = ["projects", "tools", "models", "mcps", "skills"]

// ── page ──────────────────────────────────────────────────────────────────────

export function AnalyticsPage() {
  const { t }             = useTranslation()
  const [tab, setTab]     = useState<TabId>("projects")
  const [range, setRange] = useState<Range>("all")
  // Empty range → undefined → no since param → all-time. The same filter scopes
  // the insights charts and every tab's listing, so the chips drive the whole page.
  const filter: AFilter   = range === "all" ? undefined : { since: range }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">{t("page.analytics.title")}</h1>
        <RangeChips range={range} onChange={setRange} />
      </div>

      <InsightsSection range={range} filter={filter} />

      <Tabs.Root
        value={tab}
        onValueChange={(v) => setTab(v as TabId)}
        className="space-y-3"
      >
        <Tabs.List
          className="flex w-fit max-w-full gap-1 overflow-x-auto rounded-lg border border-border bg-muted/40 p-1"
          aria-label={t("page.analytics.tabsLabel")}
        >
          {TABS.map((id) => (
            <Tabs.Tab
              key={id}
              value={id}
              className="rounded-md px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground aria-selected:bg-background aria-selected:text-foreground aria-selected:shadow-sm"
            >
              {t(`page.analytics.tabs.${id}`)}
            </Tabs.Tab>
          ))}
        </Tabs.List>

        <Tabs.Panel value="projects" className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ProjectsTab filter={filter} /></Tabs.Panel>
        <Tabs.Panel value="tools"    className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ToolsTab filter={filter} /></Tabs.Panel>
        <Tabs.Panel value="models"   className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ModelsTab filter={filter} /></Tabs.Panel>
        <Tabs.Panel value="mcps"     className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><MCPsTab filter={filter} /></Tabs.Panel>
        <Tabs.Panel value="skills"   className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><SkillsTab filter={filter} /></Tabs.Panel>
      </Tabs.Root>
    </div>
  )
}
