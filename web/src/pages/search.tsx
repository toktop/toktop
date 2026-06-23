import { useTranslation } from "react-i18next"

export function SearchPage() {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.search")}</h1>
    </div>
  )
}
