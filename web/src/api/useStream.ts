import { useEffect, useRef, useState } from "react"
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
  // session content / status — the exact set the daemon emits as named events
  // (grep the Go side: any new name must be added here or the browser drops it).
  "session.activity",
  "session.error",
  "session.idle",
  "session.status.busy",
  "session.status.idle",
  "session.status.retry",
] as const

export type LiveEventType = (typeof LIVE_EVENT_TYPES)[number]

export type StreamStatus = "connecting" | "live" | "reconnecting"

export interface UseStreamOptions {
  onResync?: () => void
}

export function useStream(
  onEvent:  (ev: LiveEvent) => void,
  opts?:    UseStreamOptions,
): StreamStatus {
  // Connection state for a live indicator. A drop flips to "reconnecting"; the
  // browser auto-reconnects transient drops and the effect manually reconnects
  // fatal (non-2xx) ones, so the next open returns to "live".
  const [status, setStatus] = useState<StreamStatus>("connecting")
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
    let es: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | null = null
    let stopped = false

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

    const connect = () => {
      const src = new EventSource("/v1/stream", { withCredentials: true })
      es = src
      src.onopen = () => setStatus("live")
      src.onerror = () => {
        setStatus("reconnecting")
        // A non-2xx response (proxy 502 while the daemon is down/idle-stopped, or a
        // 401) is fatal per the SSE spec: readyState goes CLOSED and the browser
        // does NOT auto-reconnect. Tear the dead source down and reconnect manually
        // with a small backoff so the live UI recovers instead of freezing on
        // "reconnecting". A transient drop keeps readyState CONNECTING and the
        // browser reconnects on its own — leave that path alone.
        if (src.readyState === EventSource.CLOSED && !stopped && retry === null) {
          src.close()
          retry = setTimeout(() => { retry = null; if (!stopped) connect() }, 2000)
        }
      }

      // Control frames — hello signals replay complete; resync_required means the
      // client should refetch all queries. ping is ignored.
      src.addEventListener("hello",           () => { setStatus("live"); onResyncRef.current?.() })
      src.addEventListener("resync_required", () => { onResyncRef.current?.() })
      // replay.error is a server-side replay failure hint; log and ignore.
      src.addEventListener("replay.error", (e) => {
        console.debug("[stream] replay.error", (e as MessageEvent).data)
      })
      for (const name of LIVE_EVENT_TYPES) {
        src.addEventListener(name, handler)
      }
    }

    connect()
    return () => {
      stopped = true
      if (retry !== null) clearTimeout(retry)
      es?.close()
    }
  }, [])

  return status
}
