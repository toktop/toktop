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
  const { t } = useTranslation()

  return (
    <div className="flex min-h-svh">
      {/* Sidebar nav */}
      <nav
        aria-label={t("nav.label")}
        className="flex w-56 shrink-0 flex-col gap-1 border-e border-border bg-sidebar px-3 py-4"
      >
        <div className="mb-4 px-2 text-base font-semibold tracking-tight text-sidebar-foreground">
          toktop
        </div>

        {NAV_ITEMS.map(({ to, labelKey, icon: Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) =>
              cn(
                "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm font-medium transition-colors",
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
      </nav>

      {/* Main content area */}
      <div className="flex flex-1 flex-col">
        {/* Top bar */}
        <header className="flex h-12 shrink-0 items-center justify-end gap-2 border-b border-border bg-background px-4">
          <LanguageSwitcher />
          <ThemeToggle />
        </header>

        {/* Page content */}
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
