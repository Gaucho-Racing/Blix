import { costCategoryLabels, costResourceDetail, formatUSD, type ResourceCost } from "@/lib/costs"

interface ShareBarProps {
  value: number
  total: number
}

export function ShareBar({ value, total }: ShareBarProps) {
  const share = total > 0 ? Math.min(1, value / total) : 0
  return (
    <span className="cost-share" aria-hidden="true">
      <span style={{ width: `${Math.max(share * 100, value > 0 ? 2 : 0)}%` }} />
    </span>
  )
}

interface ResourceCostTableProps {
  resources: ResourceCost[]
  emptyMessage: string
}

export function ResourceCostTable({ resources, emptyMessage }: ResourceCostTableProps) {
  if (resources.length === 0) return <div className="empty-state">{emptyMessage}</div>
  const total = resources.reduce((sum, resource) => sum + resource.cost_usd, 0)
  return (
    <div className="run-table-wrap">
      <table className="run-table cost-table">
        <thead>
          <tr><th>Resource</th><th>Usage</th><th>Share</th><th className="cost-amount">Cost</th></tr>
        </thead>
        <tbody>
          {resources.map((resource) => (
            <tr key={`${resource.category}:${resource.resource}`}>
              <td>
                <strong className="table-primary">{resource.resource}</strong>
                <span className="table-secondary">{costCategoryLabels[resource.category]}</span>
              </td>
              <td>
                <span className="table-primary">{costResourceDetail(resource)}</span>
                {resource.unpriced_calls > 0 && (
                  <span className="table-secondary cost-unpriced">{resource.unpriced_calls.toLocaleString()} calls not in the price table</span>
                )}
              </td>
              <td><ShareBar total={total} value={resource.cost_usd} /></td>
              <td className="cost-amount">{formatUSD(resource.cost_usd)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
