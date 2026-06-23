import { cn } from "@/lib/utils"

// ── status badge ──────────────────────────────────────────────────────────────

type StatusVariant = "active" | "waiting" | "idle" | "success" | "error" | "default"

function statusVariant(s: string): StatusVariant {
  switch (s) {
    case "active":
    case "busy":
    case "running":
      return "active"
    case "awaiting_confirmation":
    case "awaiting":
    case "waiting":
      return "waiting"
    case "idle":
      return "idle"
    case "success":
    case "completed":
      return "success"
    case "failed":
    case "error":
      return "error"
    default:
      return "default"
  }
}

const badgeClasses: Record<StatusVariant, string> = {
  active:  "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  waiting: "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300",
  idle:    "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
  success: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
  error:   "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
  default: "bg-muted text-muted-foreground",
}

export function StatusBadge({ status }: { status: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        badgeClasses[statusVariant(status)],
      )}
    >
      {status}
    </span>
  )
}
