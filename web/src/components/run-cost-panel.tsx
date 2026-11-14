import { useQuery } from "@tanstack/react-query"

import { ResourceCostTable, ShareBar } from "@/components/cost-tables"
import { formatUSD, getRunCosts } from "@/lib/costs"
import { errorMessage } from "@/lib/runtime"

export function RunCostPanel({ runID }: { runID: string }) {
  const query = useQuery({
    queryKey: ["run-costs", runID],
    queryFn: ({ signal }) => getRunCosts(runID, signal),
    refetchInterval: 5000,
  })
  const breakdown = query.data
  if (query.isError) return <p className="runtime-error" role="alert">{errorMessage(query.error)}</p>
  if (!breakdown) return null
  const sessionTotal = breakdown.sessions.reduce((sum, session) => sum + session.cost_usd, 0)
  return (
    <details className="panel cost-panel run-cost-panel">
      <summary className="panel-toolbar">
        <div>
          <h2>Cost · {formatUSD(breakdown.total.cost_usd)}</h2>
          <span>
            {breakdown.total.calls.toLocaleString()} billable calls
            {breakdown.total.unpriced_calls > 0 && ` · ${breakdown.total.unpriced_calls.toLocaleString()} not in the price table`}
            {" "}· price table {breakdown.pricing_version}
          </span>
        </div>
      </summary>
      <ResourceCostTable emptyMessage="No billable usage recorded for this run yet." resources={breakdown.resources} />
      {breakdown.sessions.length > 1 && (
        <ul className="cost-list">
          {breakdown.sessions.map((session) => (
            <li key={session.session_id}>
              <div>
                <code>{session.profile_id}</code>
                <span>{session.session_id}</span>
              </div>
              <ShareBar total={sessionTotal} value={session.cost_usd} />
              <strong>{formatUSD(session.cost_usd)}</strong>
            </li>
          ))}
        </ul>
      )}
    </details>
  )
}
