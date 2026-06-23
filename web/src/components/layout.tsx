import { useEffect, useRef, useState } from "react"
import type React from "react"
import { NavLink, Outlet } from "react-router-dom"
import { useTranslation } from "react-i18next"
import {
  LayoutDashboard,
  List,
  Search,
  BarChart2,
  Activity,
  Settings,
  Menu,
  X,
} from "lucide-react"

import { ThemeToggle } from "@/components/theme-toggle"
import { cn } from "@/lib/utils"

type NavItem = {
  to:       string
  labelKey: string
  icon:     React.FC<{ className?: string }>
  end?:     boolean
}

const NAV_ITEMS: NavItem[] = [
  { to: "/",          labelKey: "nav.dashboard", icon: LayoutDashboard, end: true },
  { to: "/sessions",  labelKey: "nav.sessions",  icon: List },
  { to: "/search",    labelKey: "nav.search",    icon: Search },
  { to: "/analytics", labelKey: "nav.analytics", icon: BarChart2 },
  { to: "/daemon",    labelKey: "nav.daemon",    icon: Activity },
  { to: "/settings",  labelKey: "nav.settings",  icon: Settings },
]

// NavLinks renders the shared nav-item list, reused by the desktop sidebar and
// the mobile drawer. onNavigate lets the drawer close itself on selection.
function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      {NAV_ITEMS.map(({ to, labelKey, icon: Icon, end }) => (
        <NavLink
          key={to}
          to={to}
          end={end}
          onClick={onNavigate}
          className={({ isActive }) =>
            cn(
              "flex items-center gap-2.5 rounded-md px-2 py-2.5 text-sm font-medium transition-colors",
              isActive
                ? "bg-sidebar-primary text-sidebar-primary-foreground"
                : "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
            )
          }
        >
          <Icon className="size-4 shrink-0" aria-hidden="true" />
          {t(labelKey)}
        </NavLink>
      ))}
    </>
  )
}

function LanguageSwitcher() {
  const { i18n, t } = useTranslation()

  // Only one locale now; the select will expand as locales are added.
  const locales: { code: string; labelKey: string }[] = [
    { code: "en", labelKey: "lang.en" },
  ]

  return (
    <select
      aria-label={t("lang.switcher")}
      value={i18n.language}
      onChange={(e) => void i18n.changeLanguage(e.target.value)}
      className="cursor-pointer rounded-md border border-border bg-background px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/50"
    >
      {locales.map((l) => (
        <option key={l.code} value={l.code}>
          {t(l.labelKey)}
        </option>
      ))}
    </select>
  )
}

export function Layout() {
  const { t }                   = useTranslation()
  const [drawerOpen, setDrawer] = useState(false)
  const triggerRef              = useRef<HTMLButtonElement>(null)
  const closeRef                = useRef<HTMLButtonElement>(null)

  // While the drawer is open: Esc closes it, body scroll is locked, and focus
  // moves into the drawer; on close, focus returns to the menu trigger. (Tapping
  // a nav link closes the drawer via NavLinks' onNavigate, so no route effect.)
  useEffect(() => {
    if (!drawerOpen) return
    const trigger = triggerRef.current
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setDrawer(false) }
    document.addEventListener("keydown", onKey)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = "hidden"
    closeRef.current?.focus()
    return () => {
      document.removeEventListener("keydown", onKey)
      document.body.style.overflow = prevOverflow
      trigger?.focus()
    }
  }, [drawerOpen])

  return (
    <div className="flex min-h-svh">
      {/* Desktop sidebar (md+) */}
      <nav
        aria-label={t("nav.label")}
        className="hidden w-56 shrink-0 flex-col gap-1 border-e border-border bg-sidebar px-3 py-4 md:flex"
      >
        <div className="mb-4 px-2 text-base font-semibold tracking-tight text-sidebar-foreground">
          toktop
        </div>
        <NavLinks />
      </nav>

      {/* Main column — min-w-0 lets wide tables scroll inside instead of
          forcing the whole layout wider than the viewport on mobile. */}
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile app bar (< md) */}
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-background px-3 md:hidden">
          <button
            ref={triggerRef}
            type="button"
            onClick={() => setDrawer(true)}
            aria-label={t("nav.openMenu")}
            aria-expanded={drawerOpen}
            className="-ms-1 inline-flex size-9 items-center justify-center rounded-md text-foreground hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <Menu className="size-5" aria-hidden="true" />
          </button>
          <span className="text-base font-semibold tracking-tight">toktop</span>
          <div className="ms-auto flex items-center gap-2">
            <LanguageSwitcher />
            <ThemeToggle />
          </div>
        </header>

        {/* Desktop top bar (md+) */}
        <header className="hidden h-12 shrink-0 items-center justify-end gap-2 border-b border-border bg-background px-4 md:flex">
          <LanguageSwitcher />
          <ThemeToggle />
        </header>

        {/* Page content */}
        <main className="flex-1 overflow-auto p-4 md:p-6">
          <Outlet />
        </main>
      </div>

      {/* Mobile drawer (< md) */}
      {drawerOpen && (
        <div className="fixed inset-0 z-50 md:hidden">
          <div
            className="absolute inset-0 bg-black/50"
            onClick={() => setDrawer(false)}
            aria-hidden="true"
          />
          <nav
            aria-label={t("nav.label")}
            className="absolute inset-y-0 start-0 flex w-64 max-w-[80%] flex-col gap-1 border-e border-border bg-sidebar px-3 py-4 shadow-xl"
          >
            <div className="mb-4 flex items-center justify-between px-2">
              <span className="text-base font-semibold tracking-tight text-sidebar-foreground">
                toktop
              </span>
              <button
                ref={closeRef}
                type="button"
                onClick={() => setDrawer(false)}
                aria-label={t("nav.closeMenu")}
                className="inline-flex size-8 items-center justify-center rounded-md text-sidebar-foreground hover:bg-sidebar-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                <X className="size-5" aria-hidden="true" />
              </button>
            </div>
            <NavLinks onNavigate={() => setDrawer(false)} />
          </nav>
        </div>
      )}
    </div>
  )
}
