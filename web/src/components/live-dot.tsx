import { useTranslation } from "react-i18next"

import type { StreamStatus } from "@/api/useStream"
import { cn } from "@/lib/utils"

// LiveDot is the shared SSE-connection indicator: a pulsing dot + label, used by
// the dashboard, the session detail, and the events page so the "Live" /
// "Reconnecting" affordance can't drift between them.
export function LiveDot({ status }: { status: StreamStatus }) {
  const { t } = useTranslation()
  const live = status === "live"
  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground" aria-live="polite">
      <span
        className={cn(
          "inline-block size-1.5 rounded-full",
          live ? "bg-green-500 motion-safe:animate-pulse" : "bg-yellow-500",
        )}
        aria-hidden="true"
      />
      {live ? t("common.live") : t("common.reconnecting")}
    </span>
  )
}
