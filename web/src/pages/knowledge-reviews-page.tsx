import { useInfiniteQuery } from "@tanstack/react-query"
import { isAxiosError } from "axios"
import {
  ArrowRight,
  GitPullRequest,
  LoaderCircle,
  RefreshCw,
  ShieldCheck,
} from "lucide-react"
import { Link, useSearchParams } from "react-router-dom"

import { SpendOverview } from "@/components/spend-overview"
import { StatusBadge } from "@/components/status-badge"
import { formatTimestamp } from "@/lib/format"
import { listChangesets } from "@/lib/knowledge"

const filters = [
  ["proposed", "Proposed"],
  ["", "All changes"],
  ["committed", "Published"],
  ["rejected", "Rejected"],
  ["failed", "Failed"],
  ["prepared", "Prepared"],
] as const

function errorMessage(error: unknown) {
  if (isAxiosError(error)) {
    if (error.response?.status === 403)
      return "BlixAdmins membership is required to review knowledge changes."
    const message = error.response?.data?.error
    if (typeof message === "string") return message
  }
  return "Could not reach the knowledge service. Refresh to check the latest state before retrying."
}

export function KnowledgeReviewsPage() {
  const [params, setParams] = useSearchParams()
  const requested = params.get("status") ?? "proposed"
  const status = filters.some(([value]) => value === requested)
    ? requested
    : "proposed"
  const query = useInfiniteQuery({
    queryKey: ["knowledge-changesets", status],
    queryFn: ({ pageParam }) => listChangesets(status, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  })
  const changesets = query.data?.pages.flatMap((page) => page.changesets) ?? []

  return (
    <div className="page knowledge-reviews-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · KNOWLEDGE</div>
          <h1>Memory changes</h1>
          <p>
            Review what Blix has learned before it becomes shared team
            knowledge.
          </p>
        </div>
        <button
          className="secondary-button"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
          type="button"
        >
          <RefreshCw className={query.isFetching ? "spin" : ""} size={15} />{" "}
          Refresh
        </button>
      </header>
      <div className="knowledge-review-notice">
        <ShieldCheck size={19} />
        <div>
          <strong>A second set of eyes</strong>
          <p>
            Approve to publish the exact proposed files. Reject to keep them out
            of Blix's memory. Blix can still publish supported updates directly.
          </p>
        </div>
      </div>
      <SpendOverview
        category="embedding"
        description="Indexing documents and wiki pages, plus search queries"
        title="Embedding spend"
      />
      <section className="panel knowledge-review-list">
        <div
          className="knowledge-review-filters"
          aria-label="Filter changesets"
        >
          {filters.map(([value, label]) => (
            <button
              aria-pressed={status === value}
              className={status === value ? "active" : ""}
              key={value}
              onClick={() => setParams({ status: value })}
              type="button"
            >
              {label}
            </button>
          ))}
        </div>
        {query.isPending ? (
          <Loading label="Loading memory changes" />
        ) : (
          <>
            {query.isError && (
              <div className="error-state" role="alert">
                {errorMessage(query.error)}
              </div>
            )}
            {!query.isError && changesets.length === 0 && (
              <div className="knowledge-review-empty">
                <GitPullRequest size={30} />
                <h2>
                  {status === "proposed"
                    ? "Nothing waiting for review"
                    : "No changes here yet"}
                </h2>
                <p>
                  Ask Blix to remember something about the team. Proposed memory
                  updates will appear here.
                </p>
              </div>
            )}
            {changesets.map((changeset) => (
              <Link
                className="knowledge-review-row"
                key={changeset.changeset_id}
                to={`/admin/knowledge/changesets/${changeset.changeset_id}`}
              >
                <span className="knowledge-review-row-icon">
                  <GitPullRequest size={19} />
                </span>
                <div className="knowledge-review-row-copy">
                  <div>
                    <strong>{changeset.summary}</strong>
                    <StatusBadge status={changeset.status} />
                  </div>
                  <code>{changeset.changeset_id}</code>
                  <p>
                    {changeset.season_slug || "Season-independent"}
                    <span>·</span>
                    {changeset.type}
                    <span>·</span>
                    {formatTimestamp(changeset.created_at)}
                  </p>
                </div>
                <ArrowRight size={18} />
              </Link>
            ))}
            {query.hasNextPage && (
              <div className="knowledge-review-list-footer">
                <button
                  className="secondary-button"
                  disabled={query.isFetching}
                  onClick={() => void query.fetchNextPage()}
                  type="button"
                >
                  {query.isFetchingNextPage ? "Loading…" : "Load older changes"}
                </button>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  )
}

function Loading({ label }: { label: string }) {
  return (
    <div className="loading-state">
      <LoaderCircle className="spin" size={18} /> {label}
    </div>
  )
}
