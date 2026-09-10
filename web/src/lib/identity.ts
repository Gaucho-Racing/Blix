import { useQuery } from "@tanstack/react-query"

import { resolveIdentities } from "@/lib/api"
import type { IdentitySummary } from "@/lib/types"

const noIdentities: IdentitySummary[] = []

export function useIdentities(entityIDs: string[]) {
  const normalizedIDs = [...new Set(entityIDs.filter(Boolean))].sort()
  const query = useQuery({
    queryKey: ["identities", normalizedIDs],
    queryFn: () => resolveIdentities(normalizedIDs),
    enabled: normalizedIDs.length > 0,
    staleTime: 5 * 60 * 1000,
  })
  return {
    ...query,
    identities: query.data ?? noIdentities,
  }
}
