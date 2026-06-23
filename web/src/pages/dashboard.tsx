import { useTranslation } from "react-i18next"
import { useLiveStatus } from "@/api/queries"

export function DashboardPage() {
  const { t } = useTranslation()
  const { data, isLoading } = useLiveStatus()

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.dashboard")}</h1>
      {isLoading && <p className="text-muted-foreground">{t("common.loading")}</p>}
      {data && data.items.length === 0 && (
        <p className="text-muted-foreground">{t("common.empty")}</p>
      )}
    </div>
  )
}
