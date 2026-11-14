import { api } from "@/lib/api"

export type CostCategory = "model" | "tool" | "embedding"

export interface CostTotals {
  cost_usd: number
  calls: number
  unpriced_calls: number
}

export interface DailyCost {
  day: string
  category: CostCategory
  resource: string
  cost_usd: number
  calls: number
}

export interface ResourceCost {
  category: CostCategory
  resource: string
  calls: number
  unpriced_calls: number
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  units: number
  cost_usd: number
}

export interface ProfileCost {
  profile_id: string
  calls: number
  cost_usd: number
}

export interface CallerCost {
  caller_entity_id: string
  runs: number
  cost_usd: number
}

export interface RunCost {
  run_id: string
  name: string
  kind: string
  caller_entity_id: string
  created_at: string
  cost_usd: number
}

export interface CostSummary {
  pricing_version: string
  currency: string
  days: number
  since: string
  period: CostTotals
  today: CostTotals
  last_7_days: CostTotals
  daily: DailyCost[]
  resources: ResourceCost[]
  profiles: ProfileCost[]
  callers: CallerCost[]
  runs: RunCost[]
}

export interface SessionCost {
  session_id: string
  profile_id: string
  calls: number
  cost_usd: number
}

export interface RunCostBreakdown {
  run_id: string
  pricing_version: string
  currency: string
  total: CostTotals
  resources: ResourceCost[]
  sessions: SessionCost[]
}

export const costCategoryLabels: Record<CostCategory, string> = {
  model: "Models",
  tool: "Tools",
  embedding: "Embeddings",
}

export const costCategoryOrder: CostCategory[] = ["model", "tool", "embedding"]

export async function getCostSummary(days: number, category?: CostCategory): Promise<CostSummary> {
  const { data } = await api.get<CostSummary>("/v1/admin/costs", { params: { days, category } })
  return data
}

export async function getRunCosts(runID: string, signal?: AbortSignal): Promise<RunCostBreakdown> {
  const { data } = await api.get<RunCostBreakdown>(`/v1/admin/runs/${runID}/costs`, { signal })
  return data
}

const usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 })
const preciseUSD = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 })

export function formatUSD(value: number): string {
  if (value > 0 && value < 0.01) return value < 0.0001 ? "<$0.0001" : preciseUSD.format(value)
  return usd.format(value)
}

export function costResourceDetail(resource: ResourceCost): string {
  if (resource.category === "tool") return `${resource.units.toLocaleString()} credits · ${resource.calls.toLocaleString()} calls`
  const cached = resource.cached_input_tokens > 0 ? ` (${compact(resource.cached_input_tokens)} cached)` : ""
  const output = resource.category === "embedding" ? "" : ` · ${compact(resource.output_tokens)} out`
  return `${compact(resource.input_tokens)} in${cached}${output}`
}

function compact(value: number): string {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value)
}
