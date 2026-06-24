import { useEffect, useState } from "react"
import { useTranslation }       from "react-i18next"
import { useForm }              from "@tanstack/react-form"
import { z }                    from "zod"

import { useConfig, useSetConfig } from "@/api/queries"
import { ApiError }                from "@/api/client"
import type { ConfigSetting }      from "@/api/types"
import { Button }                  from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { cn }                      from "@/lib/utils"

// ── zod validators (mirror server's config.SetKey) ───────────────────────────

const onOffSchema = z.enum(["on", "off"])

// Simplified IANA/Go timezone validation: allow "", "utc", "local", or a zone
// containing a slash (e.g. America/New_York) — the server stays authoritative.
const timezoneSchema = z.string().max(64).refine(
  // utc/local are case-insensitive on the server (normalizeTimezone lower-cases
  // before matching); accept "" (unset) and slash zones too. Server authoritative.
  (v) => v === "" || /^(utc|local)$/i.test(v) || /^[A-Za-z]+\/[A-Za-z_/]+$/.test(v),
  { message: "invalid_timezone" },
)

// Go duration: one or more <number><unit> segments — accepts both single-unit
// ("5s", "1m") and the compound canonical form the daemon returns via
// Duration.String() ("1m0s", "1h30m0s"); "" means unset (server default).
// zod is UX-only pre-validation; the server stays authoritative.
const intervalSchema = z.string().max(32).refine(
  (v) => v === "" || /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(v),
  { message: "invalid_interval" },
)

// ── single source of truth: per-key kind + validator ─────────────────────────

type KeyKind = "onoff" | "timezone" | "interval"

const KEY_META: Record<string, { kind: KeyKind; schema: z.ZodTypeAny }> = {
  redact:    { kind: "onoff",    schema: onOffSchema    },
  autostart: { kind: "onoff",    schema: onOffSchema    },
  idle_stop: { kind: "onoff",    schema: onOffSchema    },
  timezone:  { kind: "timezone", schema: timezoneSchema },
  interval:  { kind: "interval", schema: intervalSchema },
}

function validateField(key: string, value: string): string | undefined {
  const meta = KEY_META[key]
  if (!meta) return undefined
  const r = meta.schema.safeParse(value)
  if (r.success) return undefined
  return `validation.${meta.kind === "onoff" ? "onOff" : meta.kind}`
}

// ── shared card shell ─────────────────────────────────────────────────────────

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-card">
      <h2 className="border-b border-border px-4 py-2.5 text-sm font-semibold">{title}</h2>
      <div className="divide-y divide-border/50">{children}</div>
    </section>
  )
}

// ── single editable field row ─────────────────────────────────────────────────

interface EditableFieldProps {
  fieldKey:   string
  setting:    ConfigSetting
}

function EditableField({ fieldKey, setting }: EditableFieldProps) {
  const { t }      = useTranslation()
  const setConfig  = useSetConfig()
  const [serverError, setServerError] = useState<string | undefined>()
  const [savedKey, setSavedKey] = useState<string | undefined>()

  const isBoolKey = KEY_META[fieldKey]?.kind === "onoff"
  // `items` lets base-ui's <SelectValue> show the chosen label ("On"/"Off"), not
  // the raw value ("on"/"off"); labels live here once and feed the rendered items too.
  const boolItems: Record<string, string> = { on: t("page.settings.options.on"), off: t("page.settings.options.off") }

  const form = useForm({
    defaultValues: { value: setting.value },
    onSubmit: async ({ value: formValues }) => {
      setServerError(undefined)
      setSavedKey(undefined)
      try {
        await setConfig.mutateAsync({ key: fieldKey, value: formValues.value })
        setSavedKey(fieldKey)
        setTimeout(() => setSavedKey(undefined), 2000)
      } catch (err) {
        if (err instanceof ApiError) {
          setServerError(err.message)
        } else {
          setServerError(t("common.error"))
        }
      }
    },
  })

  // Resync the form when the server value changes — after a save the daemon
  // returns a canonicalized value (e.g. interval "1m" → "1m0s") that differs from
  // what was typed; without this the field keeps the stale input and the Save
  // button stays enabled (isDirty never clears). The dep only changes on refetch,
  // so it never clobbers in-progress typing.
  useEffect(() => {
    form.reset({ value: setting.value })
  }, [form, setting.value])

  const sourceLabel = setting.source === "default"
    ? t("page.settings.source.default")
    : t("page.settings.source.file")

  return (
    <form
      className="px-4 py-3"
      onSubmit={(e) => {
        e.preventDefault()
        void form.handleSubmit()
      }}
    >
      <div className="flex items-start justify-between gap-4">
        {/* label + description */}
        <div className="min-w-0 flex-1">
          <label
            htmlFor={`setting-${fieldKey}`}
            className="block text-sm font-medium leading-5"
          >
            {t(`page.settings.keys.${fieldKey}`)}
          </label>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t(`page.settings.desc.${fieldKey}`)}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground/60">
            {t("page.settings.source.label")}: <span className="font-mono">{sourceLabel}</span>
          </p>
        </div>

        {/* control + save */}
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          <form.Field
            name="value"
            validators={{
              onChange: ({ value }) => {
                const key = validateField(fieldKey, value)
                return key ? t(`page.settings.${key}`) : undefined
              },
            }}
          >
            {(field) => {
              const hasFieldError = field.state.meta.isTouched && field.state.meta.errors.length > 0
              const fieldErrId    = `err-${fieldKey}`
              const serrId        = `serr-${fieldKey}`
              const describedBy   = [
                hasFieldError ? fieldErrId : "",
                serverError   ? serrId     : "",
              ].filter(Boolean).join(" ") || undefined
              const hasAnyError   = hasFieldError || !!serverError
              return (
                <>
                  {isBoolKey ? (
                    <Select
                      items={boolItems}
                      value={field.state.value}
                      onValueChange={(v) => field.handleChange(v as string)}
                    >
                      <SelectTrigger
                        size="sm"
                        className="w-28"
                        id={`setting-${fieldKey}`}
                        aria-describedby={describedBy}
                        aria-invalid={hasAnyError || undefined}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {Object.entries(boolItems).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  ) : (
                    <input
                      id={`setting-${fieldKey}`}
                      name={field.name}
                      value={field.state.value}
                      onBlur={field.handleBlur}
                      onChange={(e) => field.handleChange(e.target.value)}
                      aria-describedby={describedBy}
                      aria-invalid={hasAnyError || undefined}
                      className={cn(
                        "h-8 w-48 rounded-md border border-border bg-background px-2 font-mono text-sm",
                        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/30",
                        hasFieldError ? "border-destructive" : "",
                      )}
                      autoComplete="off"
                      spellCheck={false}
                    />
                  )}
                  {hasFieldError && (
                    <p id={fieldErrId} className="text-xs text-destructive" role="alert">
                      {field.state.meta.errors[0]}
                    </p>
                  )}
                  {/* server-side error inline — id here so aria-describedby resolves */}
                  {serverError && (
                    <p id={serrId} className="mt-1.5 text-xs text-destructive" role="alert">
                      {serverError}
                    </p>
                  )}
                </>
              )
            }}
          </form.Field>

          <form.Subscribe selector={(s) => ({ canSubmit: s.canSubmit, isDirty: s.isDirty })}>
            {({ canSubmit, isDirty }) => (
              <Button
                type="submit"
                size="sm"
                variant="outline"
                disabled={!canSubmit || !isDirty || setConfig.isPending}
              >
                {setConfig.isPending
                  ? t("page.settings.saving")
                  : savedKey === fieldKey
                    ? t("page.settings.saved")
                    : t("page.settings.save")}
              </Button>
            )}
          </form.Subscribe>
        </div>
      </div>
    </form>
  )
}

// ── read-only field row ───────────────────────────────────────────────────────

function ReadOnlyField({ fieldKey, setting }: EditableFieldProps) {
  const { t } = useTranslation()
  const sourceLabel = setting.source === "default"
    ? t("page.settings.source.default")
    : t("page.settings.source.file")

  return (
    <div className="px-4 py-3">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium leading-5">
            {t(`page.settings.keys.${fieldKey}`)}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t(`page.settings.desc.${fieldKey}`)}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground/60">
            {t("page.settings.source.label")}: <span className="font-mono">{sourceLabel}</span>
          </p>
          <p className="mt-1 text-xs text-muted-foreground italic">
            {t("page.settings.readonly.cliOnly")}
          </p>
        </div>
        <span className="font-mono text-sm text-foreground/80 shrink-0 break-all">
          {setting.value || "—"}
        </span>
      </div>
    </div>
  )
}

// ── roots read-only block ─────────────────────────────────────────────────────

function RootsCard({ roots }: { roots: Record<string, string[]> }) {
  const { t } = useTranslation()
  return (
    <Card title={t("page.settings.roots.title")}>
      <div className="px-4 py-3">
        <p className="text-xs text-muted-foreground italic mb-2">
          {t("page.settings.roots.cliOnly")}
        </p>
        {Object.keys(roots).length === 0 ? (
          <p className="text-sm text-muted-foreground">—</p>
        ) : (
          <div className="space-y-2">
            {Object.entries(roots).map(([provider, paths]) => (
              <div key={provider}>
                <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-0.5">
                  {provider}
                </p>
                <ul className="space-y-0.5">
                  {(paths ?? []).map((p) => (
                    <li key={p} className="font-mono text-xs text-foreground/80 break-all">{p}</li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        )}
      </div>
    </Card>
  )
}

// ── page ──────────────────────────────────────────────────────────────────────

// Ordered display — editable keys first, then read-only
const EDITABLE_KEYS  = ["redact", "autostart", "idle_stop", "timezone", "interval"]
const READONLY_KEYS  = ["addr"]

export function SettingsPage() {
  const { t }  = useTranslation()
  const config = useConfig()
  const cfgErr = config.error as Error | null

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t("page.settings.title")}</h1>

      {config.isLoading && (
        <p className="text-sm text-muted-foreground">{t("page.settings.loading")}</p>
      )}
      {cfgErr && (
        <p className="text-sm text-destructive" role="alert">
          {cfgErr.message ?? t("page.settings.error")}
        </p>
      )}

      {config.data?.settings && (() => {
        const settings = config.data.settings
        const editableEntries = EDITABLE_KEYS
          .filter((k) => k in settings && settings[k].editable)
          .map((k) => [k, settings[k]] as [string, ConfigSetting])

        const readonlyEntries = READONLY_KEYS
          .filter((k) => k in settings && !settings[k].editable)
          .map((k) => [k, settings[k]] as [string, ConfigSetting])

        return (
          <>
            {editableEntries.length > 0 && (
              <Card title={t("page.settings.editable.title")}>
                {editableEntries.map(([k, s]) => (
                  <EditableField key={k} fieldKey={k} setting={s} />
                ))}
              </Card>
            )}

            {readonlyEntries.length > 0 && (
              <Card title={t("page.settings.readonly.title")}>
                {readonlyEntries.map(([k, s]) => (
                  <ReadOnlyField key={k} fieldKey={k} setting={s} />
                ))}
              </Card>
            )}
          </>
        )
      })()}

      {config.data?.roots && (
        <RootsCard roots={config.data.roots} />
      )}
    </div>
  )
}
