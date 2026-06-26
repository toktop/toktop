/* eslint-disable react-refresh/only-export-components */
import { createContext, useContext, useMemo, useState } from "react"
import type { MouseEvent, ReactNode } from "react"
import { Tooltip } from "@base-ui/react/tooltip"

// One shared tooltip for the whole app, re-anchored to whichever truncated
// element the pointer is over. A truncated cell reports itself (via
// useOverflowTooltip) only when its text is actually clipped, and the popup
// wraps so even a very long value is shown in full — which the native `title`
// (now unreliable) and the single-line ellipsis could not do. A single instance
// keeps virtualized tables cheap: no tooltip per visible cell.

type Tip = { anchor: HTMLElement; text: string }
type OverflowTooltip = {
  show: (anchor: HTMLElement, text: string) => void
  hide: () => void
}

const OverflowTooltipContext = createContext<OverflowTooltip | null>(null)

export function OverflowTooltipProvider({ children }: { children: ReactNode }) {
  const [tip, setTip] = useState<Tip | null>(null)

  // Stable value + a stable `children` element reference (mounted once at the
  // root) means show/hide re-renders only the popup below, never the app tree.
  const ctx = useMemo<OverflowTooltip>(
    () => ({
      show: (anchor, text) => setTip({ anchor, text }),
      hide: () => setTip(null),
    }),
    [],
  )

  return (
    <OverflowTooltipContext.Provider value={ctx}>
      {children}
      <Tooltip.Root open={!!tip} onOpenChange={(open) => !open && setTip(null)}>
        <Tooltip.Portal>
          <Tooltip.Positioner anchor={tip?.anchor} side="top" sideOffset={6} className="isolate z-50">
            <Tooltip.Popup className="z-50 max-w-md rounded-xl bg-foreground px-3 py-1.5 text-xs break-words whitespace-pre-wrap text-background shadow-md">
              {tip?.text}
            </Tooltip.Popup>
          </Tooltip.Positioner>
        </Tooltip.Portal>
      </Tooltip.Root>
    </OverflowTooltipContext.Provider>
  )
}

// Spread the returned handlers onto any single-line truncated element; the
// tooltip appears (with its full text) only while the pointer is over an element
// whose content is actually clipped.
export function useOverflowTooltip() {
  const ctx = useContext(OverflowTooltipContext)
  return useMemo(
    () => ({
      onMouseEnter: (e: MouseEvent<HTMLElement>) => {
        const el = e.currentTarget
        if (el.scrollWidth > el.clientWidth + 1) ctx?.show(el, el.textContent ?? "")
        else ctx?.hide()
      },
      onMouseLeave: () => ctx?.hide(),
    }),
    [ctx],
  )
}
