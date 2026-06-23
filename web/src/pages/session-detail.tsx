import { useParams } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { useSession } from "@/api/queries"

export function SessionDetailPage() {
  const { id = "" } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const { data, isLoading } = useSession(id)

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.session.title")}</h1>
      {isLoading && <p className="text-muted-foreground">{t("common.loading")}</p>}
      {data && (
        <p className="text-sm text-muted-foreground">
          {data.session.title ?? data.session.id}
        </p>
      )}
    </div>
  )
}
