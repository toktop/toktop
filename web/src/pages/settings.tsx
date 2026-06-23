import { useTranslation } from "react-i18next"

export function SettingsPage() {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.settings")}</h1>
    </div>
  )
}
