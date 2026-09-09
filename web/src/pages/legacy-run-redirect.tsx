import { Navigate, useParams } from "react-router-dom"

export function LegacyRunRedirect() {
  const { runID } = useParams()
  return <Navigate replace to={runID ? `/admin/runs/${runID}` : "/admin/runs"} />
}
