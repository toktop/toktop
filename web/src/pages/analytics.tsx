import { useTranslation } from "react-i18next"

export function AnalyticsPage() {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.analytics")}</h1>
    </div>
  )
}
