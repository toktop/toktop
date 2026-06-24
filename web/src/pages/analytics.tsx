import { useRef, useState } from "react"
import type { ReactNode }   from "react"
import { useTranslation }   from "react-i18next"
import { Link }             from "react-router-dom"
import { Tabs }             from "@base-ui/react/tabs"
import { useVirtualizer }   from "@tanstack/react-virtual"

import { reltime, fmtMs }  from "@/lib/format"
import { DataTable }       from "@/components/data-table"
import type { Column }     from "@/components/data-table"
import { StatusBadge }     from "@/components/status-badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
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
  MCPListItem,
  ModelListItem,
  ProjectListItem,
  SkillListItem,
  ToolCallListItem,
  ToolListItem,
} from "@/api/types"

// ── helpers ───────────────────────────────────────────────────────────────────

function n(v: number): string {
  return v.toLocaleString()
}

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

type TabState = { loading: boolean; error: Error | null }

function tabStatus(
  state: TabState,
  empty: boolean,
  emptyLabel: string,
  t: (key: string) => string,
): ReactNode | null {
  if (state.loading) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
  if (state.error)   return <p className="text-sm text-destructive" role="alert">{state.error.message ?? t("common.error")}</p>
  if (empty)         return <p className="text-sm text-muted-foreground">{emptyLabel}</p>
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

// ── projects tab ──────────────────────────────────────────────────────────────

function ProjectsTab() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useProjects()
  const items: ProjectListItem[]   = data ?? []

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ProjectListItem>[] = [
    {
      header: t("page.analytics.projects.name"), width: "w-[30%]",
      cell: (p) => (
        <span className="block truncate" title={p.path ?? p.name}>
          <span className="font-medium">{p.name}</span>
          {p.path && <span className="ml-2 font-mono text-xs text-muted-foreground">{p.path}</span>}
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

function ToolsTab() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useTools()
  const items: ToolListItem[]      = data ?? []
  const [drill, setDrill]          = useState<ToolDrill | null>(null)

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ToolListItem>[] = [
    {
      header: t("page.analytics.tools.name"), width: "w-[18%]",
      cell: (tc) => <span className="block truncate font-mono font-medium" title={tc.name}>{tc.name}</span>,
    },
    {
      header: t("page.analytics.tools.kind"), width: "w-[11%]",
      cell: (tc) => <span className="text-xs text-muted-foreground">{tc.kind}</span>,
    },
    {
      header: t("page.analytics.tools.mcpServer"), width: "w-[19%]",
      cell: (tc) => <span className="block truncate text-xs text-muted-foreground" title={tc.mcp_server ?? ""}>{tc.mcp_server ?? "—"}</span>,
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
      <ToolCallsDialog drill={drill} onClose={() => setDrill(null)} />
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

function ToolCallsDialog({ drill, onClose }: { drill: ToolDrill | null; onClose: () => void }) {
  const { t }      = useTranslation()
  const tool       = drill?.tool ?? null
  const kindStatus = drill?.status ?? "failed"
  const total      = tool ? (kindStatus === "failed" ? tool.failed_count : tool.rejected_count) : 0

  const { data, isLoading, error } = useToolCalls(
    { name: tool?.name ?? "", kind: tool?.kind, mcp_server: tool?.mcp_server, status: kindStatus },
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

function ModelsTab() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useModels()
  const items: ModelListItem[]     = data ?? []

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)
  if (status) return status

  const columns: Column<ModelListItem>[] = [
    {
      header: t("page.analytics.models.model"), width: "w-[16%]",
      cell: (m) => <span className="block truncate font-mono font-medium" title={m.model}>{m.model || "—"}</span>,
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

function MCPsTab() {
  const { t }                       = useTranslation()
  const [unusedOnly, setUnusedOnly] = useState(false)
  const all                         = useMcps()
  const unused                      = useUnusedMcps()
  const active                      = unusedOnly ? unused : all
  const items: MCPListItem[]        = active.data ?? []

  const toggle = <UnusedToggle label={t("page.analytics.mcps.unusedOnly")} checked={unusedOnly} onChange={setUnusedOnly} />

  const ts: TabState = { loading: active.isLoading, error: active.error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)
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
      cell: (m) => <span className="block truncate font-mono font-medium" title={m.server}>{m.server || "—"}</span>,
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

function SkillsTab() {
  const { t }                       = useTranslation()
  const [unusedOnly, setUnusedOnly] = useState(false)
  const all                         = useSkills()
  const unused                      = useUnusedSkills()
  const active                      = unusedOnly ? unused : all
  const items: SkillListItem[]      = active.data ?? []

  const toggle = <UnusedToggle label={t("page.analytics.skills.unusedOnly")} checked={unusedOnly} onChange={setUnusedOnly} />

  const ts: TabState = { loading: active.isLoading, error: active.error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)
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
        <span className="block truncate" title={s.name}>
          <span className="font-mono font-medium">{s.name}</span>
          {s.version && <span className="ml-1 text-[10px] text-muted-foreground">v{s.version}</span>}
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
      cell: (s) => <span className="block truncate text-xs text-muted-foreground" title={s.description ?? ""}>{s.description || "—"}</span>,
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
  const { t }         = useTranslation()
  const [tab, setTab] = useState<TabId>("projects")

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.analytics.title")}</h1>

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

        <Tabs.Panel value="projects" className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ProjectsTab /></Tabs.Panel>
        <Tabs.Panel value="tools"    className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ToolsTab /></Tabs.Panel>
        <Tabs.Panel value="models"   className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><ModelsTab /></Tabs.Panel>
        <Tabs.Panel value="mcps"     className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><MCPsTab /></Tabs.Panel>
        <Tabs.Panel value="skills"   className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"><SkillsTab /></Tabs.Panel>
      </Tabs.Root>
    </div>
  )
}
