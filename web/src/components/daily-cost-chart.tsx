import { useLayoutEffect, useMemo, useRef, useState } from "react"

import { costCategoryLabels, costCategoryOrder, formatUSD, type CostCategory, type DailyCost } from "@/lib/costs"

const seriesColor: Record<CostCategory, string> = {
  model: "var(--series-1)",
  tool: "var(--series-2)",
  embedding: "var(--series-3)",
}

const margin = { top: 12, right: 8, bottom: 26, left: 52 }
const maxBarWidth = 24
const segmentGap = 2
const capRadius = 4

interface Day {
  day: string
  totals: Record<CostCategory, number>
  total: number
}

interface DailyCostChartProps {
  daily: DailyCost[]
  since: string
  days: number
  categories?: CostCategory[]
  label: string
  height?: number
}

function dayKeys(since: string, days: number): string[] {
  const start = new Date(`${since.slice(0, 10)}T00:00:00Z`)
  return Array.from({ length: days }, (_, index) => {
    const day = new Date(start)
    day.setUTCDate(start.getUTCDate() + index)
    return day.toISOString().slice(0, 10)
  })
}

function niceMax(value: number): number {
  if (value <= 0) return 1
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const step = [1, 2, 2.5, 5, 10].find((candidate) => candidate * magnitude >= value) ?? 10
  return step * magnitude
}

function dayLabel(day: string, style: "short" | "long" = "short"): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
    ...(style === "long" ? { weekday: "short" } : {}),
  }).format(new Date(`${day}T00:00:00Z`))
}

function columnPath(x: number, y: number, width: number, barHeight: number, rounded: boolean): string {
  const r = rounded ? Math.min(capRadius, width / 2, barHeight) : 0
  return `M${x},${y + barHeight}V${y + r}Q${x},${y} ${x + r},${y}H${x + width - r}Q${x + width},${y} ${x + width},${y + r}V${y + barHeight}Z`
}

export function DailyCostChart({ daily, since, days, categories = costCategoryOrder, label, height = 220 }: DailyCostChartProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(640)
  const [active, setActive] = useState<number | null>(null)
  const [showTable, setShowTable] = useState(false)

  useLayoutEffect(() => {
    const element = containerRef.current
    if (!element) return
    const observer = new ResizeObserver(([entry]) => setWidth(Math.max(280, Math.floor(entry.contentRect.width))))
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const series = useMemo(
    () => categories.filter((category) => daily.some((row) => row.category === category && row.cost_usd > 0)),
    [categories, daily],
  )

  const rows = useMemo<Day[]>(() => {
    const byDay = new Map<string, Day>()
    for (const day of dayKeys(since, days)) {
      byDay.set(day, { day, totals: { model: 0, tool: 0, embedding: 0 }, total: 0 })
    }
    for (const row of daily) {
      const target = byDay.get(row.day)
      if (!target || !categories.includes(row.category)) continue
      target.totals[row.category] += row.cost_usd
      target.total += row.cost_usd
    }
    return [...byDay.values()]
  }, [categories, daily, days, since])

  const plotWidth = width - margin.left - margin.right
  const plotHeight = height - margin.top - margin.bottom
  const band = plotWidth / Math.max(rows.length, 1)
  const barWidth = Math.max(2, Math.min(maxBarWidth, band * 0.62))
  const top = niceMax(Math.max(...rows.map((row) => row.total), 0))
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((fraction) => top * fraction)
  const scale = (value: number) => (value / top) * plotHeight
  const labelEvery = Math.max(1, Math.ceil(rows.length / Math.max(2, Math.floor(plotWidth / 64))))
  const activeRow = active === null ? null : rows[active]
  const tooltipLeft = active === null ? 0 : Math.min(Math.max(margin.left + band * (active + 0.5), 90), width - 90)

  return (
    <div className="cost-chart" ref={containerRef}>
      <div className="cost-chart-header">
        {series.length > 1 && (
          <ul className="cost-chart-legend" aria-label="Legend">
            {series.map((category) => (
              <li key={category}>
                <span className="cost-chart-swatch" style={{ background: seriesColor[category] }} />
                {costCategoryLabels[category]}
              </li>
            ))}
          </ul>
        )}
        <button className="cost-chart-toggle" onClick={() => setShowTable((value) => !value)} type="button">
          {showTable ? "Show chart" : "Show table"}
        </button>
      </div>

      {showTable ? (
        <div className="cost-chart-table-wrap">
          <table className="cost-chart-table">
            <caption className="sr-only">{label}</caption>
            <thead>
              <tr>
                <th scope="col">Day (UTC)</th>
                {series.map((category) => <th key={category} scope="col">{costCategoryLabels[category]}</th>)}
                <th scope="col">Total</th>
              </tr>
            </thead>
            <tbody>
              {[...rows].reverse().map((row) => (
                <tr key={row.day}>
                  <th scope="row">{dayLabel(row.day, "long")}</th>
                  {series.map((category) => <td key={category}>{formatUSD(row.totals[category])}</td>)}
                  <td>{formatUSD(row.total)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="cost-chart-plot">
          <svg aria-label={label} height={height} role="group" width={width}>
            <g>
              {ticks.map((tick) => {
                const y = margin.top + plotHeight - scale(tick)
                return (
                  <g key={tick}>
                    <line className="cost-chart-grid" x1={margin.left} x2={width - margin.right} y1={y} y2={y} />
                    <text className="cost-chart-axis" textAnchor="end" x={margin.left - 8} y={y + 3}>
                      {formatUSD(tick)}
                    </text>
                  </g>
                )
              })}
            </g>
            {rows.map((row, index) => {
              const x = margin.left + band * index + (band - barWidth) / 2
              let cursor = margin.top + plotHeight
              const visible = series.filter((category) => row.totals[category] > 0)
              return (
                <g
                  aria-label={`${dayLabel(row.day, "long")}: ${formatUSD(row.total)}`}
                  className="cost-chart-column"
                  data-active={active === index || undefined}
                  key={row.day}
                  onBlur={() => setActive(null)}
                  onFocus={() => setActive(index)}
                  onPointerEnter={() => setActive(index)}
                  onPointerLeave={() => setActive(null)}
                  role="img"
                  tabIndex={0}
                >
                  <rect className="cost-chart-hit" height={plotHeight + margin.top} width={band} x={margin.left + band * index} y={0} />
                  {visible.map((category, segment) => {
                    const fullHeight = scale(row.totals[category])
                    const gap = segment < visible.length - 1 ? segmentGap : 0
                    const barHeight = Math.max(fullHeight - gap, 1)
                    cursor -= fullHeight
                    return (
                      <path
                        d={columnPath(x, cursor + gap, barWidth, barHeight, segment === visible.length - 1)}
                        fill={seriesColor[category]}
                        key={category}
                      />
                    )
                  })}
                  {index % labelEvery === 0 && (
                    <text className="cost-chart-axis" textAnchor="middle" x={x + barWidth / 2} y={height - 8}>
                      {dayLabel(row.day)}
                    </text>
                  )}
                </g>
              )
            })}
            <line className="cost-chart-baseline" x1={margin.left} x2={width - margin.right} y1={margin.top + plotHeight} y2={margin.top + plotHeight} />
          </svg>
          {activeRow && (
            <div className="cost-chart-tooltip" role="status" style={{ left: tooltipLeft }}>
              <strong>{formatUSD(activeRow.total)}</strong>
              <span>{dayLabel(activeRow.day, "long")} · UTC</span>
              {series.length > 1 && (
                <dl>
                  {series.map((category) => (
                    <div key={category}>
                      <dt><span className="cost-chart-swatch" style={{ background: seriesColor[category] }} />{costCategoryLabels[category]}</dt>
                      <dd>{formatUSD(activeRow.totals[category])}</dd>
                    </div>
                  ))}
                </dl>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
