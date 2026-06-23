import { Moon, Sun, Monitor } from "lucide-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { useTheme } from "@/components/theme-provider"

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const { t } = useTranslation()

  const cycle = () => {
    if (theme === "light")  setTheme("dark")
    else if (theme === "dark") setTheme("system")
    else setTheme("light")
  }

  const icon =
    theme === "light"  ? <Sun  className="size-4" /> :
    theme === "dark"   ? <Moon className="size-4" /> :
                         <Monitor className="size-4" />

  const label =
    theme === "light"  ? t("theme.light") :
    theme === "dark"   ? t("theme.dark")  :
                         t("theme.system")

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={cycle}
      aria-label={`${t("theme.toggle")}: ${label}`}
      title={`${t("theme.toggle")}: ${label}`}
    >
      {icon}
      <span className="sr-only">{t("theme.toggle")}</span>
    </Button>
  )
}
