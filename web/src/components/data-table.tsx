import { useMemo, useRef, useState } from "react"
import type { ReactNode }            from "react"
import { useTranslation }            from "react-i18next"

import { VirtualTable }  from "./virtual-table"
import type { Column }   from "./virtual-table"

// Re-export so existing callers keep importing the column type from here.
export type { Column } from "./virtual-table"

type DataTableProps<T> = {
  columns:       Column<T>[]
  rows:          T[]
  rowKey:        (row: T, i: number) => string
  filterText:    (row: T) => string   // haystack for the client-side text filter
  minWidth?:     string               // optional min-width class for wide tables
  toolbarExtra?: ReactNode            // sits on the toolbar next to the search box
}

// A searchable wrapper over VirtualTable. The caller gates loading/error/no-data;
// this renders only when there is data, so the lone empty state here is "filter
// matched nothing". The virtualized rendering lives in VirtualTable.
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  filterText,
  minWidth,
  toolbarExtra,
}: DataTableProps<T>) {
  const { t }     = useTranslation()
  const [q, setQ] = useState("")

  // Callers pass `filterText` as a fresh inline arrow each render, so keep the
  // latest in a ref and memoize the filter on (rows, q) only — otherwise the memo's
  // deps change identity every parent render and the full scan re-runs needlessly.
  const filterTextRef = useRef(filterText)
  // Writing/reading the ref during render is the deliberate latest-ref escape hatch
  // that keeps the memo keyed on (rows, q) only; the compiler's refs check flags it
  // as it does the incompatible-library hook above — both are intentional here.
  // eslint-disable-next-line react-hooks/refs
  filterTextRef.current = filterText
  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    if (!needle) return rows
    // eslint-disable-next-line react-hooks/refs
    return rows.filter((r) => filterTextRef.current(r).toLowerCase().includes(needle))
  }, [rows, q])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <input
          type="search"
          autoComplete="off"
          spellCheck={false}
          className="h-9 w-full max-w-xs rounded-lg border border-input bg-background px-3 text-sm focus:ring-2 focus:ring-ring focus:outline-none"
          placeholder={t("page.analytics.filters.search")}
          aria-label={t("page.analytics.filters.search")}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {toolbarExtra}
      </div>

      {filtered.length === 0 ? (
        <p className="rounded-lg border border-border px-4 py-8 text-center text-sm text-muted-foreground">
          {t("page.analytics.filters.noResults", { q: q.trim() })}
        </p>
      ) : (
        <VirtualTable columns={columns} rows={filtered} rowKey={rowKey} minWidth={minWidth} />
      )}
    </div>
  )
}
