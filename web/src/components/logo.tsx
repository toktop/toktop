import { cn } from "@/lib/utils"

// The toktop brand mark, served from public/logo.png (a 1:1 gradient blob with the
// pulse waveform baked in — it reads on both light and dark surfaces). alt="" marks
// it decorative; the adjacent wordmark in <Brand> carries the accessible name.
export function Logo({ className }: { className?: string }) {
  return <img src="/logo.png" alt="" className={cn("object-contain", className)} />
}

// The full brand lockup: the mark plus a plain "toktop" wordmark. Used in every
// brand spot (sidebar, mobile bar, drawer); callers add only layout spacing.
export function Brand({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <Logo className="size-6" />
      <span className="text-base font-semibold tracking-tight text-foreground">toktop</span>
    </span>
  )
}
