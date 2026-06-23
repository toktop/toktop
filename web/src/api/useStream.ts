import { useEffect, useRef } from "react"
import type { LiveEvent } from "./types"

// LIVE_EVENT_TYPES lists every SSE event name the server sends as a named event
// (i.e. with an "event: <name>" line). EventSource has no wildcard/catch-all
// listener — onmessage only fires for un-named (default) events, which the
// toktop daemon does NOT emit for live payloads. Every new server event type
// MUST be added here, or it will be silently dropped by the browser.
export const LIVE_EVENT_TYPES = [
  // daemon lifecycle
  "daemon.log",
  "daemon.state",
  // hook intake
  "hook.intake",
  // ingest
  "ingest.full",
  "ingest.session",
  // session content / status
  "session.activity",
  "session.created",
  "session.error",
  "session.idle",
  "session.updated",
  "session.status",
  "session.status.busy",
  "session.status.idle",
  "session.status.retry",
  // defensive aliases (older/alternate event names some providers may emit)
  "session.active",
  "session.success",
  "session.failed",
] as const

export type LiveEventType = (typeof LIVE_EVENT_TYPES)[number]

export interface UseStreamOptions {
  onResync?: () => void
}

export function useStream(
  onEvent:  (ev: LiveEvent) => void,
  opts?:    UseStreamOptions,
): void {
  // Store callbacks in refs so the effect closure always calls the latest
  // version without re-subscribing. Refs are written inside the effect, not
  // during render, to satisfy the react-hooks/refs rule.
  const onEventRef  = useRef<(ev: LiveEvent) => void>(onEvent)
  const onResyncRef = useRef<(() => void) | undefined>(opts?.onResync)

  useEffect(() => {
    onEventRef.current  = onEvent
    onResyncRef.current = opts?.onResync
  })

  useEffect(() => {
    const es = new EventSource("/v1/stream", { withCredentials: true })

    // Control frames — hello signals replay complete; resync_required means the
    // client should refetch all queries. ping is ignored.
    es.addEventListener("hello",           () => { onResyncRef.current?.() })
    es.addEventListener("resync_required", () => { onResyncRef.current?.() })
    // replay.error is a server-side replay failure hint; log and ignore.
    es.addEventListener("replay.error", (e) => {
      console.debug("[stream] replay.error", (e as MessageEvent).data)
    })

    // Data frames — each named live event type gets its own listener because
    // EventSource provides no wildcard mechanism. Parse the JSON payload and
    // forward to the caller via onEvent.
    const handler = (e: Event) => {
      try {
        onEventRef.current(JSON.parse((e as MessageEvent).data) as LiveEvent)
      } catch {
        // malformed JSON; ignore
      }
    }

    for (const name of LIVE_EVENT_TYPES) {
      es.addEventListener(name, handler)
    }

    return () => {
      es.close()
    }
  }, [])
}
