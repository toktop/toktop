import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { useForm }         from "@tanstack/react-form"
import { parseISO } from "date-fns"
import { z }               from "zod"
import { ChevronUp, ChevronDown, ChevronsUpDown } from "lucide-react"

import { useSessions, useSources } from "@/api/queries"
import type { Session } from "@/api/types"
import { StatusBadge } from "@/components/status-badge"
import { reltime, fmtTokens } from "@/lib/format"

// ── constants ─────────────────────────────────────────────────────────────────

const PAGE_LIMIT = 25

type SortKey = "started" | "turns"
type SortDir = "asc" | "desc"

// ── zod filter schema ─────────────────────────────────────────────────────────

const filterSchema = z.object({
  sources:    z.string().optional(),
  project:    z.string().optional(),
  status:     z.string().optional(),
  since:      z.string().optional(),
  subagents:  z.boolean(),
})

type FilterValues = z.infer<typeof filterSchema>

const defaultFilter: FilterValues = {
  sources:   "",
  project:   "",
  status:    "",
  since:     "",
  subagents: false,
}

// ── filter bar ────────────────────────────────────────────────────────────────

interface FilterBarProps {
  onSubmit: (v: FilterValues) => void
  initial:  FilterValues
}

function FilterBar({ onSubmit, initial }: FilterBarProps) {
  const { t } = useTranslation()
  const { data: sources } = useSources()
  // distinct provider names (handleSources returns one row per provider+root);
  // the server folds the name to a source_id via ResolveSourceToken.
  const sourceOptions = [...new Set((sources ?? []).map((s) => s.source))]

  const form = useForm({
    defaultValues: initial,
    validators:    { onChange: filterSchema },
    onSubmit:      ({ value }) => onSubmit(value),
  })

  return (
    <form
      className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-card p-4"
      onSubmit={(e) => { e.preventDefault(); void form.handleSubmit() }}
    >
      {/* sources */}
      <form.Field name="sources">
        {(field) => (
          <div className="flex flex-col gap-1">
            <label htmlFor={field.name} className="text-xs text-muted-foreground">
              {t("page.sessions.filters.sources")}
            </label>
            <select
              id={field.name}
              className="h-8 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              value={field.state.value ?? ""}
              onChange={(e) => field.handleChange(e.target.value)}
            >
              <option value="">{t("page.sessions.filters.sourcesAll")}</option>
              {sourceOptions.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </div>
        )}
      </form.Field>

      {/* project */}
      <form.Field name="project">
        {(field) => (
          <div className="flex flex-col gap-1">
            <label htmlFor={field.name} className="text-xs text-muted-foreground">
              {t("page.sessions.filters.project")}
            </label>
            <input
              id={field.name}
              className="h-8 w-40 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              placeholder={t("page.sessions.filters.projectPH")}
              value={field.state.value ?? ""}
              onChange={(e) => field.handleChange(e.target.value)}
            />
          </div>
        )}
      </form.Field>

      {/* status */}
      <form.Field name="status">
        {(field) => (
          <div className="flex flex-col gap-1">
            <label htmlFor={field.name} className="text-xs text-muted-foreground">
              {t("page.sessions.filters.status")}
            </label>
            <select
              id={field.name}
              className="h-8 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              value={field.state.value ?? ""}
              onChange={(e) => field.handleChange(e.target.value)}
            >
              <option value="">{t("page.sessions.filters.statusAll")}</option>
              <option value="completed">{t("page.sessions.filters.statusCompleted")}</option>
              <option value="unknown">{t("page.sessions.filters.statusUnknown")}</option>
            </select>
          </div>
        )}
      </form.Field>

      {/* since */}
      <form.Field name="since">
        {(field) => (
          <div className="flex flex-col gap-1">
            <label htmlFor={field.name} className="text-xs text-muted-foreground">
              {t("page.sessions.filters.since")}
            </label>
            <input
              id={field.name}
              className="h-8 w-28 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              placeholder={t("page.sessions.filters.sincePH")}
              value={field.state.value ?? ""}
              onChange={(e) => field.handleChange(e.target.value)}
            />
          </div>
        )}
      </form.Field>

      {/* subagents toggle */}
      <form.Field name="subagents">
        {(field) => (
          <label className="flex h-8 cursor-pointer items-center gap-2 text-sm select-none">
            <input
              type="checkbox"
              className="h-4 w-4 rounded border-input accent-primary"
              checked={field.state.value}
              onChange={(e) => field.handleChange(e.target.checked)}
            />
            {t("page.sessions.filters.subagents")}
          </label>
        )}
      </form.Field>

      {/* submit */}
      <button
        type="submit"
        className="h-8 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground hover:bg-primary/80"
      >
        {t("page.sessions.filters.apply")}
      </button>
    </form>
  )
}

// ── sort header button ────────────────────────────────────────────────────────

interface SortButtonProps {
  label:    string
  colKey:   SortKey
  current:  SortKey
  dir:      SortDir
  onChange: (k: SortKey, d: SortDir) => void
}

function SortButton({ label, colKey, current, dir, onChange }: SortButtonProps) {
  const { t } = useTranslation()
  const active = current === colKey
  const next   = active && dir === "desc" ? "asc" : "desc"

  return (
    <button
      type="button"
      className="flex items-center gap-1 font-medium hover:text-foreground"
      onClick={() => onChange(colKey, next)}
      aria-label={t("page.sessions.sortBy", { column: label })}
    >
      {label}
      {active
        ? dir === "asc"
          ? <ChevronUp className="size-3" />
          : <ChevronDown className="size-3" />
        : <ChevronsUpDown className="size-3 opacity-40" />}
    </button>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

export function SessionsPage() {
  const { t }        = useTranslation()
  const navigate     = useNavigate()

  const [filter, setFilter]   = useState<FilterValues>(defaultFilter)
  const [offset, setOffset]   = useState(0)
  const [sortKey, setSortKey] = useState<SortKey>("started")
  const [sortDir, setSortDir] = useState<SortDir>("desc")

  // build query params from filter + sort + pagination
  const params: Record<string, string | number | boolean | undefined> = {
    limit:     PAGE_LIMIT,
    offset,
    sort:      `${sortKey}_${sortDir}`,
    sources:   filter.sources   || undefined,
    project:   filter.project   || undefined,
    status:    filter.status    || undefined,
    since:     filter.since     || undefined,
    subagents: filter.subagents ? 1 : undefined,
  }

  const { data, isLoading, error } = useSessions(params)

  function applyFilter(v: FilterValues) {
    setFilter(v)
    setOffset(0)   // reset pagination on new filter
  }

  function changeSort(k: SortKey, d: SortDir) {
    setSortKey(k)
    setSortDir(d)
    setOffset(0)
  }

  const showSubagentCol = filter.subagents

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.sessions.title")}</h1>

      <FilterBar onSubmit={applyFilter} initial={defaultFilter} />

      {/* loading / error / empty */}
      {isLoading && (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      )}
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error.message ?? t("common.error")}
        </p>
      )}
      {!isLoading && !error && data && data.items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("page.sessions.empty")}</p>
      )}

      {/* table */}
      {data && data.items.length > 0 && (
        <>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="border-b border-border bg-muted/50 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-4 py-2 text-left font-medium">
                    {t("page.sessions.columns.title")}
                  </th>
                  <th scope="col" className="px-4 py-2 text-left font-medium">
                    {t("page.sessions.columns.provider")}
                  </th>
                  <th scope="col" className="px-4 py-2 text-left font-medium">
                    {t("page.sessions.columns.status")}
                  </th>
                  {showSubagentCol && (
                    <th scope="col" className="px-4 py-2 text-left font-medium">
                      {t("page.sessions.columns.kind")}
                    </th>
                  )}
                  <th
                    scope="col"
                    className="px-4 py-2 text-right font-medium"
                    aria-sort={sortKey === "turns" ? (sortDir === "asc" ? "ascending" : "descending") : "none"}
                  >
                    <SortButton
                      label={t("page.sessions.columns.turns")}
                      colKey="turns"
                      current={sortKey}
                      dir={sortDir}
                      onChange={changeSort}
                    />
                  </th>
                  <th scope="col" className="px-4 py-2 text-right font-medium">
                    {t("page.sessions.columns.tools")}
                  </th>
                  <th scope="col" className="px-4 py-2 text-right font-medium">
                    {t("page.sessions.columns.tokens")}
                  </th>
                  <th
                    scope="col"
                    className="px-4 py-2 text-right font-medium"
                    aria-sort={sortKey === "started" ? (sortDir === "asc" ? "ascending" : "descending") : "none"}
                  >
                    <SortButton
                      label={t("page.sessions.columns.started")}
                      colKey="started"
                      current={sortKey}
                      dir={sortDir}
                      onChange={changeSort}
                    />
                  </th>
                  <th scope="col" className="px-4 py-2 text-right font-medium">
                    {t("page.sessions.columns.duration")}
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((s) => (
                  <SessionRow
                    key={s.id}
                    session={s}
                    showKind={showSubagentCol}
                    onClick={() => navigate(`/sessions/${s.id}`)}
                  />
                ))}
              </tbody>
            </table>
          </div>

          {/* pagination */}
          <Pagination
            offset={offset}
            limit={PAGE_LIMIT}
            total={data.total}
            nextOffset={data.next_offset}
            onPrev={() => setOffset(Math.max(0, offset - PAGE_LIMIT))}
            onNext={() => setOffset(data.next_offset)}
          />
        </>
      )}
    </div>
  )
}

// ── session row ───────────────────────────────────────────────────────────────

function SessionRow({
  session: s,
  showKind,
  onClick,
}: { session: Session; showKind: boolean; onClick: () => void }) {
  const { t } = useTranslation()
  const title = s.title ?? s.project_name ?? s.id

  return (
    <tr
      className="border-b border-border last:border-0 hover:bg-muted/40 cursor-pointer transition-colors"
      onClick={onClick}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
    >
      {/* title */}
      <td className="max-w-[200px] truncate px-4 py-2 font-medium" title={title}>
        {title}
        {s.is_subagent && (
          <span className="ml-1 rounded bg-muted px-1 py-0.5 text-[10px] text-muted-foreground">
            {t("page.sessions.subBadge")}
          </span>
        )}
      </td>

      {/* provider */}
      <td className="px-4 py-2 text-xs uppercase tracking-wide text-muted-foreground">
        {s.provider}
      </td>

      {/* status */}
      <td className="px-4 py-2">
        <StatusBadge status={s.status} />
      </td>

      {/* kind (subagents mode) */}
      {showKind && (
        <td className="px-4 py-2 text-xs text-muted-foreground">
          {s.is_subagent
            ? (s.subagent_kind ?? t("page.sessions.kindSubagent"))
            : t("page.sessions.kindTopLevel")}
        </td>
      )}

      {/* turns */}
      <td className="px-4 py-2 text-right tabular-nums">{s.turn_count}</td>

      {/* tools */}
      <td className="px-4 py-2 text-right tabular-nums">{s.tool_call_count}</td>

      {/* tokens */}
      <td className="px-4 py-2 text-right tabular-nums">{fmtTokens(s.tokens)}</td>

      {/* started */}
      <td className="px-4 py-2 text-right text-xs text-muted-foreground">
        {reltime(s.started_at)}
      </td>

      {/* duration */}
      <td className="px-4 py-2 text-right text-xs text-muted-foreground">
        {durationLabel(s.started_at, s.ended_at)}
      </td>
    </tr>
  )
}

function durationLabel(start?: string, end?: string): string {
  if (!start || !end) return "—"
  try {
    const ms = parseISO(end).getTime() - parseISO(start).getTime()
    if (ms < 0)           return "—"
    if (ms < 60_000)      return `${Math.round(ms / 1000)}s`
    if (ms < 3_600_000)   return `${Math.round(ms / 60_000)}m`
    return `${(ms / 3_600_000).toFixed(1)}h`
  } catch { return "—" }
}

// ── pagination ────────────────────────────────────────────────────────────────

interface PaginationProps {
  offset:     number
  limit:      number
  total:      number
  nextOffset: number
  onPrev:     () => void
  onNext:     () => void
}

function Pagination({ offset, limit, total, nextOffset, onPrev, onNext }: PaginationProps) {
  const { t } = useTranslation()
  const from  = offset + 1
  const to    = Math.min(offset + limit, total)
  const hasPrev = offset > 0
  const hasNext = nextOffset > 0 && nextOffset < total

  return (
    <div className="flex items-center justify-between text-sm text-muted-foreground">
      <span>
        {t("page.sessions.pagination.showing", { from, to, total })}
      </span>
      <div className="flex gap-2">
        <button
          type="button"
          disabled={!hasPrev}
          onClick={onPrev}
          className="rounded-md border border-border px-3 py-1 text-xs hover:bg-muted disabled:pointer-events-none disabled:opacity-40"
        >
          {t("page.sessions.pagination.prev")}
        </button>
        <button
          type="button"
          disabled={!hasNext}
          onClick={onNext}
          className="rounded-md border border-border px-3 py-1 text-xs hover:bg-muted disabled:pointer-events-none disabled:opacity-40"
        >
          {t("page.sessions.pagination.next")}
        </button>
      </div>
    </div>
  )
}
