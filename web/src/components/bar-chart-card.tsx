import { useMemo } from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import type { ChartConfig } from "@/components/ui/chart"

export type BarDatum = { label: string; value: number }

// A horizontal top-N bar chart in a card: categories on the Y axis, one metric
// on the X. Long category labels are clipped on the axis; the full label rides
// in the hover tooltip. Pre-sort/slice the rows before passing them in.
export function BarChartCard({
  title,
  metricLabel,
  data,
  emptyText,
}: {
  title:       string
  metricLabel: string        // legend/tooltip name for the single series
  data:        BarDatum[]
  emptyText:   string
}) {
  const config = useMemo<ChartConfig>(
    () => ({ value: { label: metricLabel, color: "var(--chart-1)" } }),
    [metricLabel],
  )
  // A hidden recharts-3.x number axis mis-derives its domain (auto blows it up,
  // the "dataMax" string collapses the bars to nothing), so pin it to a concrete
  // numeric max computed from the data.
  const maxValue = useMemo(() => Math.max(1, ...data.map((d) => d.value)), [data])

  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle className="text-sm">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {data.length === 0 ? (
          <p className="py-12 text-center text-sm text-muted-foreground">{emptyText}</p>
        ) : (
          <ChartContainer config={config} className="aspect-auto h-[240px] w-full">
            <BarChart
              accessibilityLayer
              data={data}
              layout="vertical"
              margin={{ left: 4, right: 16, top: 4, bottom: 4 }}
            >
              <CartesianGrid horizontal={false} strokeDasharray="3 3" />
              <YAxis
                dataKey="label"
                type="category"
                tickLine={false}
                axisLine={false}
                width={132}
                tickFormatter={(v: string) => (v.length > 22 ? `${v.slice(0, 21)}…` : v)}
              />
              <XAxis
                type="number"
                domain={[0, maxValue]}
                tickLine={false}
                axisLine={false}
                tick={{ fontSize: 11 }}
                allowDecimals={false}
              />
              <ChartTooltip cursor={false} content={<ChartTooltipContent />} />
              {/* isAnimationActive=false: under recharts 3.8 + React 19 the Bar's
                  enter animation never fires, leaving an empty inactive-bar (no
                  rectangle). Disabling animation renders the final shape directly. */}
              <Bar dataKey="value" fill="var(--color-value)" radius={4} isAnimationActive={false} />
            </BarChart>
          </ChartContainer>
        )}
      </CardContent>
    </Card>
  )
}
