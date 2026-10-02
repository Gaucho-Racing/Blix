import type { ExecutionStatus } from "@/lib/types"

interface StatusBadgeProps {
  status: ExecutionStatus | string
}

export function StatusBadge({ status }: StatusBadgeProps) {
  return (
    <span className={`status-badge status-${status}`}>
      <span className="status-dot" />
      {status.replaceAll("_", " ")}
    </span>
  )
}
