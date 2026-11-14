import { useQuery } from "@tanstack/react-query"
import { AlertTriangle, CalendarDays, CircleDollarSign, LoaderCircle, Sigma } from "lucide-react"
import { Link, useSearchParams } from "react-router-dom"

import { ResourceCostTable, ShareBar } from "@/components/cost-tables"
import { DailyCostChart } from "@/components/daily-cost-chart"
import { IdentityDisplay } from "@/components/identity-display"
import { formatUSD, getCostSummary } from "@/lib/costs"
import { formatTimestamp } from "@/lib/format"
import { errorMessage } from "@/lib/runtime"

const periods = [7, 30, 90] as const

export function CostsPage() {
  const [params, setParams] = useSearchParams()
  const requested = Number(params.get("days"))
  const days = periods.find((period) => period === requested) ?? 30
  const query = useQuery({
    queryKey: ["admin", "costs", days],
    queryFn: () => getCostSummary(days),
    refetchInterval: 30_000,
  })
  const summary = query.data
  const profileTotal = summary?.profiles.reduce((sum, row) => sum + row.cost_usd, 0) ?? 0
  const callerTotal = summary?.callers.reduce((sum, row) => sum + row.cost_usd, 0) ?? 0

  return (
    <div className="page costs-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · SPEND</div>
          <h1>Costs</h1>
          <p>Estimated spend on models, web tools, and embeddings, priced from the static price table.</p>
        </div>
        <div className="knowledge-review-filters cost-period-filter" role="group" aria-label="Period">
          {periods.map((period) => (
            <button
              aria-pressed={days === period}
              className={days === period ? "active" : ""}
              key={period}
              onClick={() => setParams({ days: String(period) })}
              type="button"
            >
              {period} days
            </button>
          ))}
        </div>
      </header>

      {query.isPending ? (
        <div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading costs</div>
      ) : query.isError || !summary ? (
        <div className="runtime-error" role="alert">{errorMessage(query.error)}</div>
      ) : (
        <>
          <section className="stat-grid" aria-label="Spend summary">
            <article className="stat-card"><CircleDollarSign size={17} /><div><span>Today (UTC)</span><strong>{formatUSD(summary.today.cost_usd)}</strong></div></article>
            <article className="stat-card"><CalendarDays size={17} /><div><span>Last 7 days</span><strong>{formatUSD(summary.last_7_days.cost_usd)}</strong></div></article>
            <article className="stat-card"><Sigma size={17} /><div><span>Last {days} days</span><strong>{formatUSD(summary.period.cost_usd)}</strong></div></article>
            <article className={summary.period.unpriced_calls > 0 ? "stat-card danger" : "stat-card"}>
              <AlertTriangle size={17} />
              <div><span>Unpriced calls</span><strong>{summary.period.unpriced_calls.toLocaleString()}</strong></div>
            </article>
          </section>

          <section className="panel cost-panel">
            <div className="panel-toolbar">
              <div>
                <h2>Daily spend</h2>
                <span>{summary.period.calls.toLocaleString()} billable calls · price table {summary.pricing_version}</span>
              </div>
            </div>
            <DailyCostChart daily={summary.daily} days={days} label={`Daily spend over the last ${days} days`} since={summary.since} />
          </section>

          <section className="panel cost-panel">
            <div className="panel-toolbar"><div><h2>By model and tool</h2><span>Token and credit usage behind each line item</span></div></div>
            <ResourceCostTable emptyMessage="No usage recorded in this period." resources={summary.resources} />
          </section>

          <div className="cost-split">
            <section className="panel cost-panel">
              <div className="panel-toolbar"><div><h2>By agent</h2><span>Which agent profile made the calls</span></div></div>
              {summary.profiles.length === 0 ? (
                <div className="empty-state">No usage recorded in this period.</div>
              ) : (
                <ul className="cost-list">
                  {summary.profiles.map((row) => (
                    <li key={row.profile_id || "unattributed"}>
                      <div>
                        <code>{row.profile_id || "unattributed"}</code>
                        <span>{row.calls.toLocaleString()} calls</span>
                      </div>
                      <ShareBar total={profileTotal} value={row.cost_usd} />
                      <strong>{formatUSD(row.cost_usd)}</strong>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section className="panel cost-panel">
              <div className="panel-toolbar"><div><h2>By person</h2><span>Who started the runs</span></div></div>
              {summary.callers.length === 0 ? (
                <div className="empty-state">No run usage recorded in this period.</div>
              ) : (
                <ul className="cost-list">
                  {summary.callers.map((row) => (
                    <li key={row.caller_entity_id}>
                      <div>
                        <IdentityDisplay entityID={row.caller_entity_id} />
                        <span>{row.runs.toLocaleString()} runs</span>
                      </div>
                      <ShareBar total={callerTotal} value={row.cost_usd} />
                      <strong>{formatUSD(row.cost_usd)}</strong>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>

          <section className="panel cost-panel">
            <div className="panel-toolbar"><div><h2>Most expensive runs</h2><span>Top 25 in this period</span></div></div>
            {summary.runs.length === 0 ? (
              <div className="empty-state">No run usage recorded in this period.</div>
            ) : (
              <div className="run-table-wrap">
                <table className="run-table cost-table">
                  <thead><tr><th>Run</th><th>Started by</th><th>Created</th><th className="cost-amount">Cost</th></tr></thead>
                  <tbody>
                    {summary.runs.map((run) => (
                      <tr key={run.run_id}>
                        <td>
                          <Link className="run-link" to={`/admin/runs/${run.run_id}`}>
                            <strong>{run.name || "New run"}</strong>
                            <code>{run.run_id}</code>
                          </Link>
                        </td>
                        <td><IdentityDisplay entityID={run.caller_entity_id} /></td>
                        <td>{formatTimestamp(run.created_at)}</td>
                        <td className="cost-amount">{formatUSD(run.cost_usd)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}
