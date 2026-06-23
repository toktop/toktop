import { useState }       from "react"
import type { ReactNode }  from "react"
import { useTranslation }  from "react-i18next"
import { Tabs }            from "@base-ui/react/tabs"

import { reltime }         from "@/lib/format"
import {
  useMcps,
  useModels,
  useProjects,
  useSkills,
  useSummary,
  useTools,
  useUnusedMcps,
  useUnusedSkills,
} from "@/api/queries"
import type {
  MCPListItem,
  ModelListItem,
  ProjectListItem,
  SkillListItem,
  ToolListItem,
} from "@/api/types"

// ── helpers ───────────────────────────────────────────────────────────────────

function n(v: number): string {
  return v.toLocaleString()
}

// ── shared table primitives ───────────────────────────────────────────────────

function Th({ children, right }: { children: React.ReactNode; right?: boolean }) {
  return (
    <th
      scope="col"
      className={`px-4 py-2 font-medium ${right ? "text-right" : "text-left"}`}
    >
      {children}
    </th>
  )
}

function Td({ children, right, mono }: { children: React.ReactNode; right?: boolean; mono?: boolean }) {
  return (
    <td className={`px-4 py-2 ${right ? "text-right" : ""} ${mono ? "tabular-nums" : ""}`}>
      {children}
    </td>
  )
}

function TableShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        {children}
      </table>
    </div>
  )
}

function Thead({ children }: { children: React.ReactNode }) {
  return (
    <thead className="border-b border-border bg-muted/50 text-xs text-muted-foreground">
      <tr>{children}</tr>
    </thead>
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

// ── projects tab ──────────────────────────────────────────────────────────────

function ProjectsTab() {
  const { t }                       = useTranslation()
  const { data, isLoading, error }  = useProjects()
  const items: ProjectListItem[]    = data ?? []

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)

  return status ?? (
    <TableShell>
      <Thead>
        <Th>{t("page.analytics.projects.name")}</Th>
        <Th>{t("page.analytics.projects.source")}</Th>
        <Th right>{t("page.analytics.projects.sessions")}</Th>
        <Th right>{t("page.analytics.projects.turns")}</Th>
        <Th right>{t("page.analytics.projects.tools")}</Th>
        <Th right>{t("page.analytics.projects.lastActivity")}</Th>
      </Thead>
      <tbody>
        {items.map((p) => (
          <tr key={p.id} className="border-b border-border last:border-0 hover:bg-muted/40 transition-colors">
            <Td>
              <span className="font-medium">{p.name}</span>
              {p.path && (
                <span className="ml-2 font-mono text-xs text-muted-foreground truncate max-w-[180px] inline-block align-bottom" title={p.path}>
                  {p.path}
                </span>
              )}
            </Td>
            <Td><span className="text-xs uppercase tracking-wide text-muted-foreground">{p.source_id}</span></Td>
            <Td right mono>{n(p.session_count)}</Td>
            <Td right mono>{n(p.turn_count)}</Td>
            <Td right mono>{n(p.tool_call_count)}</Td>
            <Td right><span className="text-xs text-muted-foreground">{reltime(p.last_activity)}</span></Td>
          </tr>
        ))}
      </tbody>
    </TableShell>
  )
}

// ── tools tab ─────────────────────────────────────────────────────────────────

function ToolsTab() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useTools()
  const items: ToolListItem[]      = data ?? []

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)

  return status ?? (
    <TableShell>
      <Thead>
        <Th>{t("page.analytics.tools.name")}</Th>
        <Th>{t("page.analytics.tools.kind")}</Th>
        <Th>{t("page.analytics.tools.mcpServer")}</Th>
        <Th right>{t("page.analytics.tools.calls")}</Th>
        <Th right>{t("page.analytics.tools.turns")}</Th>
        <Th right>{t("page.analytics.tools.failed")}</Th>
        <Th right>{t("page.analytics.tools.rejected")}</Th>
        <Th right>{t("page.analytics.tools.lastUsed")}</Th>
      </Thead>
      <tbody>
        {items.map((tc, i) => (
          <tr key={`${tc.kind}-${tc.name}-${tc.mcp_server ?? ""}-${i}`}
              className="border-b border-border last:border-0 hover:bg-muted/40 transition-colors">
            <Td><span className="font-mono font-medium">{tc.name}</span></Td>
            <Td><span className="text-xs text-muted-foreground">{tc.kind}</span></Td>
            <Td><span className="text-xs text-muted-foreground">{tc.mcp_server ?? "—"}</span></Td>
            <Td right mono>{n(tc.call_count)}</Td>
            <Td right mono>{n(tc.turn_count)}</Td>
            <Td right mono>
              {tc.failed_count > 0
                ? <span className="text-destructive">{n(tc.failed_count)}</span>
                : <span className="text-muted-foreground">0</span>}
            </Td>
            <Td right mono>
              {tc.rejected_count > 0
                ? <span className="text-yellow-600 dark:text-yellow-400">{n(tc.rejected_count)}</span>
                : <span className="text-muted-foreground">0</span>}
            </Td>
            <Td right><span className="text-xs text-muted-foreground">{reltime(tc.last_used_at)}</span></Td>
          </tr>
        ))}
      </tbody>
    </TableShell>
  )
}

// ── models tab ────────────────────────────────────────────────────────────────

function ModelsTab() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useModels()
  const items: ModelListItem[]     = data ?? []

  const ts: TabState = { loading: isLoading, error: error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)

  return status ?? (
    <TableShell>
      <Thead>
        <Th>{t("page.analytics.models.model")}</Th>
        <Th>{t("page.analytics.models.provider")}</Th>
        <Th right>{t("page.analytics.models.calls")}</Th>
        <Th right>{t("page.analytics.models.turns")}</Th>
        <Th right>{t("page.analytics.models.inputTokens")}</Th>
        <Th right>{t("page.analytics.models.outputTokens")}</Th>
        <Th right>{t("page.analytics.models.cacheRead")}</Th>
        <Th right>{t("page.analytics.models.cacheWrite")}</Th>
        <Th right>{t("page.analytics.models.cacheWriteLong")}</Th>
        <Th right>{t("page.analytics.models.lastUsed")}</Th>
      </Thead>
      <tbody>
        {items.map((m, i) => (
          <tr key={`${m.provider}-${m.model}-${i}`}
              className="border-b border-border last:border-0 hover:bg-muted/40 transition-colors">
            <Td><span className="font-mono font-medium">{m.model || "—"}</span></Td>
            <Td><span className="text-xs uppercase tracking-wide text-muted-foreground">{m.provider || "—"}</span></Td>
            <Td right mono>{n(m.call_count)}</Td>
            <Td right mono>{n(m.turn_count)}</Td>
            <Td right mono>{n(m.input_tokens)}</Td>
            <Td right mono>{n(m.output_tokens)}</Td>
            <Td right mono>{n(m.cache_read_tokens)}</Td>
            <Td right mono>{n(m.cache_write_tokens)}</Td>
            <Td right mono>{n(m.cache_write_long_tokens)}</Td>
            <Td right><span className="text-xs text-muted-foreground">{reltime(m.last_used_at)}</span></Td>
          </tr>
        ))}
      </tbody>
    </TableShell>
  )
}

// ── mcps tab ──────────────────────────────────────────────────────────────────

function MCPsTab() {
  const { t }                               = useTranslation()
  const [unusedOnly, setUnusedOnly]         = useState(false)
  const all                                 = useMcps()
  const unused                              = useUnusedMcps()
  const active                              = unusedOnly ? unused : all
  const items: MCPListItem[]                = active.data ?? []

  const ts: TabState = { loading: active.isLoading, error: active.error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)

  return (
    <div className="space-y-3">
      <label className="flex items-center gap-2 text-sm select-none cursor-pointer">
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-input accent-primary"
          checked={unusedOnly}
          onChange={(e) => setUnusedOnly(e.target.checked)}
        />
        {t("page.analytics.mcps.unusedOnly")}
      </label>

      {status ?? (
        <TableShell>
          <Thead>
            <Th>{t("page.analytics.mcps.server")}</Th>
            <Th>{t("page.analytics.mcps.source")}</Th>
            <Th>{t("page.analytics.mcps.declared")}</Th>
            <Th>{t("page.analytics.mcps.scope")}</Th>
            <Th right>{t("page.analytics.mcps.calls")}</Th>
            <Th right>{t("page.analytics.mcps.tools")}</Th>
            <Th right>{t("page.analytics.mcps.turns")}</Th>
            <Th right>{t("page.analytics.mcps.availability")}</Th>
            <Th right>{t("page.analytics.mcps.lastUsed")}</Th>
          </Thead>
          <tbody>
            {items.map((m, i) => (
              <tr key={`${m.source_id}-${m.server}-${i}`}
                  className="border-b border-border last:border-0 hover:bg-muted/40 transition-colors">
                <Td><span className="font-mono font-medium">{m.server || "—"}</span></Td>
                <Td><span className="text-xs uppercase tracking-wide text-muted-foreground">{m.source_id}</span></Td>
                <Td>
                  <span className={`text-xs ${m.declared ? "text-green-600 dark:text-green-400" : "text-muted-foreground"}`}>
                    {m.declared ? t("page.analytics.mcps.yes") : t("page.analytics.mcps.no")}
                  </span>
                </Td>
                <Td><span className="text-xs text-muted-foreground">{m.scope ?? "—"}</span></Td>
                <Td right mono>{n(m.call_count)}</Td>
                <Td right mono>{n(m.tool_count)}</Td>
                <Td right mono>{n(m.turn_count)}</Td>
                <Td right mono>{n(m.availability_observed)}</Td>
                <Td right><span className="text-xs text-muted-foreground">{reltime(m.last_used_at)}</span></Td>
              </tr>
            ))}
          </tbody>
        </TableShell>
      )}
    </div>
  )
}

// ── skills tab ────────────────────────────────────────────────────────────────

function SkillsTab() {
  const { t }                             = useTranslation()
  const [unusedOnly, setUnusedOnly]       = useState(false)
  const all                               = useSkills()
  const unused                            = useUnusedSkills()
  const active                            = unusedOnly ? unused : all
  const items: SkillListItem[]            = active.data ?? []

  const ts: TabState = { loading: active.isLoading, error: active.error as Error | null }
  const status = tabStatus(ts, items.length === 0, t("page.analytics.empty"), t)

  return (
    <div className="space-y-3">
      <label className="flex items-center gap-2 text-sm select-none cursor-pointer">
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-input accent-primary"
          checked={unusedOnly}
          onChange={(e) => setUnusedOnly(e.target.checked)}
        />
        {t("page.analytics.skills.unusedOnly")}
      </label>

      {status ?? (
        <TableShell>
          <Thead>
            <Th>{t("page.analytics.skills.name")}</Th>
            <Th>{t("page.analytics.skills.source")}</Th>
            <Th>{t("page.analytics.skills.scope")}</Th>
            <Th>{t("page.analytics.skills.installed")}</Th>
            <Th>{t("page.analytics.skills.description")}</Th>
            <Th right>{t("page.analytics.skills.usedCount")}</Th>
            <Th right>{t("page.analytics.skills.lastUsed")}</Th>
          </Thead>
          <tbody>
            {items.map((s, i) => (
              <tr key={`${s.source_id}-${s.name}-${i}`}
                  className="border-b border-border last:border-0 hover:bg-muted/40 transition-colors">
                <Td>
                  <span className="font-mono font-medium">{s.name}</span>
                  {s.version && (
                    <span className="ml-1 text-[10px] text-muted-foreground">v{s.version}</span>
                  )}
                </Td>
                <Td><span className="text-xs uppercase tracking-wide text-muted-foreground">{s.source_id}</span></Td>
                <Td><span className="text-xs text-muted-foreground">{s.scope ?? "—"}</span></Td>
                <Td>
                  <span className={`text-xs ${s.installed ? "text-green-600 dark:text-green-400" : "text-muted-foreground"}`}>
                    {s.installed ? t("page.analytics.skills.yes") : t("page.analytics.skills.no")}
                  </span>
                </Td>
                <Td>
                  <span className="text-xs text-muted-foreground truncate max-w-[200px] inline-block align-bottom" title={s.description}>
                    {s.description || "—"}
                  </span>
                </Td>
                <Td right mono>{n(s.inferred_used_count)}</Td>
                <Td right><span className="text-xs text-muted-foreground">{reltime(s.last_used_at)}</span></Td>
              </tr>
            ))}
          </tbody>
        </TableShell>
      )}
    </div>
  )
}

// ── summary strip ─────────────────────────────────────────────────────────────

function SummaryStrip() {
  const { t }                      = useTranslation()
  const { data, isLoading, error } = useSummary()
  if (isLoading || error || !data) return null

  const stats = [
    { key: "sessions",  value: n(data.sessions) },
    { key: "turns",     value: n(data.turns) },
    { key: "toolCalls", value: n(data.tool_calls) },
    { key: "inputTokens",  value: n(data.input_tokens) },
    { key: "outputTokens", value: n(data.output_tokens) },
  ]

  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      {stats.map(({ key, value }) => (
        <div key={key} className="rounded-lg border border-border bg-card px-4 py-3">
          <p className="text-2xl font-semibold tabular-nums">{value}</p>
          <p className="text-xs text-muted-foreground">{t(`page.analytics.summary.${key}`)}</p>
        </div>
      ))}
    </div>
  )
}

// ── tab ids ───────────────────────────────────────────────────────────────────

type TabId = "projects" | "tools" | "models" | "mcps" | "skills"
const TABS: TabId[] = ["projects", "tools", "models", "mcps", "skills"]

// ── page ──────────────────────────────────────────────────────────────────────

export function AnalyticsPage() {
  const { t }              = useTranslation()
  const [tab, setTab]      = useState<TabId>("projects")

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.analytics.title")}</h1>

      <SummaryStrip />

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
