import { useTranslation } from "react-i18next"
import { useSessions } from "@/api/queries"

export function SessionsPage() {
  const { t } = useTranslation()
  const { data, isLoading } = useSessions()

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.sessions.title")}</h1>
      {isLoading && <p className="text-muted-foreground">{t("common.loading")}</p>}
      {data && data.items.length === 0 && (
        <p className="text-muted-foreground">{t("common.empty")}</p>
      )}
    </div>
  )
}
