import { useMemo, useRef, useState } from "react"
import type { ReactNode }            from "react"
import { useTranslation }            from "react-i18next"
import { useVirtualizer }            from "@tanstack/react-virtual"

// A column descriptor. `cell` renders one row's value; `width` is the
// <colgroup> width class (table-layout: fixed, so widths are authoritative and
// don't jitter as the virtualized window scrolls). `right` right-aligns numerics.
export type Column<T> = {
  header: string
  cell:   (row: T) => ReactNode
  width:  string
  right?: boolean
}

type DataTableProps<T> = {
  columns:       Column<T>[]
  rows:          T[]
  rowKey:        (row: T, i: number) => string
  filterText:    (row: T) => string   // haystack for the client-side text filter
  minWidth?:     string               // optional min-width class for wide tables
  toolbarExtra?: ReactNode            // sits on the toolbar next to the search box
}

// Uniform single-line row height (px-4 py-2 + 1px border). Cells are nowrap +
// truncate, so every row is exactly this tall — no measureElement needed.
const ROW_H = 41

// A searchable, virtualized table. The caller gates loading/error/no-data; this
// renders only when there is data, so the lone empty state here is "filter
// matched nothing". Only the visible window of rows is in the DOM.
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
  const scrollRef = useRef<HTMLDivElement>(null)

  // Callers pass `filterText` as a fresh inline arrow each render, so keep the
  // latest in a ref and memoize the filter on (rows, q) only — otherwise the memo's
  // deps change identity every parent render and the full scan re-runs needlessly.
  const filterTextRef = useRef(filterText)
  filterTextRef.current = filterText
  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    if (!needle) return rows
    return rows.filter((r) => filterTextRef.current(r).toLowerCase().includes(needle))
  }, [rows, q])

  // useVirtualizer reads fresh scroll geometry each render and intentionally is not
  // memoized; we don't rely on React Compiler memoization here. Silence the
  // compiler's incompatible-library lint probe (the build doesn't run the compiler).
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count:            filtered.length,
    getScrollElement: () => scrollRef.current,
    estimateSize:     () => ROW_H,
    overscan:         8,
  })
  const virtualRows = virtualizer.getVirtualItems()
  const totalSize   = virtualizer.getTotalSize()
  const padTop      = virtualRows.length ? virtualRows[0].start : 0
  const padBottom   = virtualRows.length ? totalSize - virtualRows[virtualRows.length - 1].end : 0

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
        <div ref={scrollRef} className="max-h-[70vh] overflow-auto rounded-lg border border-border">
          <table className={`w-full table-fixed text-sm ${minWidth ?? ""}`}>
            <colgroup>
              {columns.map((c, i) => (
                <col key={i} className={c.width} />
              ))}
            </colgroup>
            <thead className="sticky top-0 z-10 bg-muted text-xs text-muted-foreground">
              <tr className="border-b border-border">
                {columns.map((c, i) => (
                  <th
                    key={i}
                    scope="col"
                    className={`truncate px-4 py-2 font-medium ${c.right ? "text-right" : "text-left"}`}
                  >
                    {c.header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {padTop > 0 && (
                <tr aria-hidden>
                  <td colSpan={columns.length} style={{ height: padTop }} />
                </tr>
              )}
              {virtualRows.map((vr) => {
                const row = filtered[vr.index]
                return (
                  <tr
                    key={rowKey(row, vr.index)}
                    style={{ height: ROW_H }}
                    className="border-b border-border transition-colors last:border-0 hover:bg-muted/40"
                  >
                    {columns.map((c, ci) => (
                      <td
                        key={ci}
                        className={`truncate px-4 py-2 ${c.right ? "text-right tabular-nums" : ""}`}
                      >
                        {c.cell(row)}
                      </td>
                    ))}
                  </tr>
                )
              })}
              {padBottom > 0 && (
                <tr aria-hidden>
                  <td colSpan={columns.length} style={{ height: padBottom }} />
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
