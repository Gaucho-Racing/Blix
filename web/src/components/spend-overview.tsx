import { useQuery } from "@tanstack/react-query"
import { ChevronRight, LoaderCircle } from "lucide-react"
import { Link } from "react-router-dom"

import { DailyCostChart } from "@/components/daily-cost-chart"
import { formatUSD, getCostSummary, type CostCategory } from "@/lib/costs"
import { errorMessage } from "@/lib/runtime"

interface SpendOverviewProps {
  title: string
  description: string
  category?: CostCategory
  days?: number
}

export function SpendOverview({ title, description, category, days = 30 }: SpendOverviewProps) {
  const query = useQuery({
    queryKey: ["admin", "costs", days, category ?? "all"],
    queryFn: () => getCostSummary(days, category),
    refetchInterval: 60_000,
  })
  const summary = query.data
  return (
    <section className="panel cost-panel spend-overview">
      <div className="panel-toolbar">
        <div>
          <h2>{title}</h2>
          <span>{description}</span>
        </div>
        <Link className="secondary-button" to={`/admin/costs?days=${days}`}>
          All costs <ChevronRight size={14} />
        </Link>
      </div>
      {query.isPending ? (
        <div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading spend</div>
      ) : query.isError || !summary ? (
        <div className="runtime-error" role="alert">{errorMessage(query.error)}</div>
      ) : (
        <>
          <dl className="spend-overview-figures">
            <div><dt>Today (UTC)</dt><dd>{formatUSD(summary.today.cost_usd)}</dd></div>
            <div><dt>Last 7 days</dt><dd>{formatUSD(summary.last_7_days.cost_usd)}</dd></div>
            <div><dt>Last {days} days</dt><dd>{formatUSD(summary.period.cost_usd)}</dd></div>
            <div><dt>Calls</dt><dd>{summary.period.calls.toLocaleString()}</dd></div>
          </dl>
          <DailyCostChart
            categories={category ? [category] : undefined}
            daily={summary.daily}
            days={days}
            height={180}
            label={`${title} per day over the last ${days} days`}
            since={summary.since}
          />
        </>
      )}
    </section>
  )
}
