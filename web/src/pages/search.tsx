import { useState, useEffect, useRef } from "react"
import { useNavigate }                 from "react-router-dom"
import { useTranslation }              from "react-i18next"

import { useSearch }     from "@/api/queries"
import type { SearchResult } from "@/api/types"

// ── snippet rendering ─────────────────────────────────────────────────────────
//
// The Go FTS layer calls:
//   snippet(search_fts, 6, '[', ']', '…', 12)
//
// Matched terms are wrapped in '[' … ']'.  We parse these safe, fixed delimiters
// and wrap each match in <mark> — no dangerouslySetInnerHTML needed.

function SnippetView({ text }: { text: string }) {
  // Split on '[' and ']' to extract segments.  Odd-indexed segments (inside
  // a '[…]' pair) are highlighted; even-indexed are plain text.
  const parts = text.split(/(\[[^\]]*\])/g)

  return (
    <span className="font-mono text-xs leading-relaxed break-words">
      {parts.map((part, i) => {
        if (part.startsWith("[") && part.endsWith("]")) {
          return (
            <mark
              key={i}
              className="rounded bg-yellow-200 px-0.5 text-yellow-900 dark:bg-yellow-700/60 dark:text-yellow-100"
            >
              {part.slice(1, -1)}
            </mark>
          )
        }
        return <span key={i}>{part}</span>
      })}
    </span>
  )
}

// ── kind / provider badges ────────────────────────────────────────────────────

function KindBadge({ kind }: { kind: string }) {
  const isSession = kind === "session"
  return (
    <span
      className={
        "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium " +
        (isSession
          ? "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300"
          : "bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300")
      }
    >
      {kind}
    </span>
  )
}

function ProviderBadge({ provider }: { provider: string }) {
  if (!provider) return null
  return (
    <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium text-muted-foreground uppercase tracking-wide">
      {provider}
    </span>
  )
}

// ── result item ───────────────────────────────────────────────────────────────

function ResultItem({ result, onClick }: { result: SearchResult; onClick: () => void }) {
  const idLabel = result.turn_id
    ? `${result.session_id.slice(0, 8)}… / turn ${result.turn_id.slice(0, 8)}…`
    : `${result.session_id.slice(0, 8)}…`

  return (
    <li>
      <button
        type="button"
        className="w-full rounded-lg border border-border bg-card p-4 text-left hover:bg-muted/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring transition-colors cursor-pointer"
        onClick={onClick}
      >
        {/* header row: badges + id */}
        <div className="flex flex-wrap items-center gap-2 mb-2">
          <KindBadge kind={result.kind} />
          <ProviderBadge provider={result.provider} />
          <span className="text-[10px] text-muted-foreground font-mono">{idLabel}</span>
        </div>

        {/* snippet */}
        <SnippetView text={result.snippet} />
      </button>
    </li>
  )
}

// ── debounce hook ─────────────────────────────────────────────────────────────

function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(id)
  }, [value, delay])
  return debounced
}

// ── page ──────────────────────────────────────────────────────────────────────

export function SearchPage() {
  const { t }    = useTranslation()
  const navigate = useNavigate()

  const [raw, setRaw]             = useState("")
  const [subagents, setSubagents] = useState(false)
  const [kind, setKind]           = useState("")

  const q = useDebounced(raw.trim(), 300)

  const { data, isLoading, isFetching, error } = useSearch({ q, kind: kind || undefined, subagents })

  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => { inputRef.current?.focus() }, [])

  const results  = data?.results ?? []
  const searched = q.length > 0

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.search.title")}</h1>

      {/* search box */}
      <div className="flex flex-col gap-2">
        <label htmlFor="search-input" className="sr-only">
          {t("page.search.inputLabel")}
        </label>
        <input
          ref={inputRef}
          id="search-input"
          type="search"
          autoComplete="off"
          spellCheck={false}
          className="h-10 w-full rounded-lg border border-input bg-background px-3 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          placeholder={t("page.search.inputPH")}
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
        />
      </div>

      {/* filter bar */}
      <div className="flex flex-wrap items-center gap-4">
        {/* subagents toggle */}
        <label className="flex cursor-pointer items-center gap-2 text-sm select-none">
          <input
            type="checkbox"
            className="h-4 w-4 rounded border-input accent-primary"
            checked={subagents}
            onChange={(e) => setSubagents(e.target.checked)}
          />
          {t("page.search.filters.subagents")}
        </label>

        {/* kind select */}
        <div className="flex items-center gap-2">
          <label htmlFor="kind-select" className="text-xs text-muted-foreground">
            {t("page.search.filters.kind")}
          </label>
          <select
            id="kind-select"
            className="h-7 rounded-md border border-input bg-background px-2 text-xs focus:outline-none focus:ring-2 focus:ring-ring"
            value={kind}
            onChange={(e) => setKind(e.target.value)}
          >
            <option value="">{t("page.search.filters.kindAll")}</option>
            <option value="turn">{t("page.search.filters.kindTurn")}</option>
            <option value="tool_call">{t("page.search.filters.kindToolCall")}</option>
          </select>
        </div>
      </div>

      {/* states */}
      {!searched && (
        <p className="text-sm text-muted-foreground">{t("page.search.hint")}</p>
      )}

      {searched && (isLoading || isFetching) && (
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      )}

      {searched && error && (
        <p className="text-sm text-destructive" role="alert">
          {(error as Error).message ?? t("page.search.error")}
        </p>
      )}

      {searched && !isLoading && !error && results.length === 0 && (
        <p className="text-sm text-muted-foreground">
          {t("page.search.noResults", { q })}
        </p>
      )}

      {/* results list */}
      {results.length > 0 && (
        <ol className="space-y-2" aria-label={t("page.search.title")}>
          {results.map((r) => (
            <ResultItem
              key={`${r.kind}:${r.id}`}
              result={r}
              onClick={() => navigate(`/sessions/${r.session_id}`)}
            />
          ))}
        </ol>
      )}
    </div>
  )
}
