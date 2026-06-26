import { useRef }         from "react"
import type { ReactNode } from "react"
import { useVirtualizer } from "@tanstack/react-virtual"

import { useOverflowTooltip } from "./overflow-tooltip"

// A column descriptor. `cell` renders one row's value; `width` is the
// <colgroup> width class (table-layout: fixed, so widths are authoritative and
// don't jitter as the virtualized window scrolls — leave it "" to let a column
// soak up the remaining space). `right` right-aligns numerics.
export type Column<T> = {
  header: string
  cell:   (row: T) => ReactNode
  width:  string
  right?: boolean
}

type VirtualTableProps<T> = {
  columns:   Column<T>[]
  rows:      T[]
  rowKey:    (row: T, i: number) => string
  minWidth?: string   // optional min-width class for wide tables
}

// Uniform single-line row height (px-4 py-2 + 1px border). Cells are nowrap +
// truncate, so every row is exactly this tall — no measureElement needed.
const ROW_H = 41

// The virtualized table primitive: a scrollable, fixed-layout table that keeps
// only the visible window of rows in the DOM. It renders exactly the `rows` it
// is given — empty state, search, and any toolbar are the caller's concern, so
// this stays a pure rendering surface shared by every list page.
export function VirtualTable<T>({
  columns,
  rows,
  rowKey,
  minWidth,
}: VirtualTableProps<T>) {
  const scrollRef = useRef<HTMLDivElement>(null)
  // Every cell is single-line + truncated, so the td is the one clip point: hover
  // a truncated cell to see its full value. One shared handler covers all cells.
  const tip = useOverflowTooltip()

  // useVirtualizer reads fresh scroll geometry each render and intentionally is not
  // memoized; we don't rely on React Compiler memoization here. Silence the
  // compiler's incompatible-library lint probe (the build doesn't run the compiler).
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count:            rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize:     () => ROW_H,
    overscan:         8,
  })
  const virtualRows = virtualizer.getVirtualItems()
  const totalSize   = virtualizer.getTotalSize()
  const padTop      = virtualRows.length ? virtualRows[0].start : 0
  const padBottom   = virtualRows.length ? totalSize - virtualRows[virtualRows.length - 1].end : 0

  return (
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
            const row = rows[vr.index]
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
                    onMouseEnter={tip.onMouseEnter}
                    onMouseLeave={tip.onMouseLeave}
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
  )
}
