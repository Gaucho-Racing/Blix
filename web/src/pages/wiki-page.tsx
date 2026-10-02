import { useInfiniteQuery } from "@tanstack/react-query"
import { ArrowLeft, LoaderCircle } from "lucide-react"
import { Link, useSearchParams } from "react-router-dom"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import type { DocumentCitation } from "@/lib/documents"
import { readWiki } from "@/lib/documents"
import { wikiLink } from "@/lib/wiki-links"
import { errorMessage } from "@/lib/runtime"

function Markdown({
  content,
  path,
  proposedPaths = [],
}: {
  content: string
  path: string
  proposedPaths?: string[]
}) {
  return (
    <div className="wiki-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ href, children }) => {
            if (!href) return <span>{children}</span>
            const link = wikiLink(href, path, proposedPaths)
            return link.external ? (
              <a href={link.href} target="_blank" rel="noreferrer">
                {children}
              </a>
            ) : link.href.startsWith("#") ? (
              <a href={link.href}>{children}</a>
            ) : (
              <Link to={link.href}>{children}</Link>
            )
          },
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
function EvidenceLinks({
  citations,
}: {
  citations: Record<string, DocumentCitation>
}) {
  const sources = Object.values(citations)
  if (!sources.length) return null
  return (
    <section className="wiki-evidence">
      <h2>Source evidence</h2>
      {sources.map((citation) => (
        <div key={citation.source_record_id}>
          {citation.document_id ? (
            <Link
              to={`/knowledge/documents/${citation.document_id}?page=${citation.pdf_page}`}
            >
              {citation.source_name} · PDF page {citation.pdf_page}
            </Link>
          ) : citation.external_url ? (
            <a href={citation.external_url} target="_blank" rel="noreferrer">
              {citation.source_name}
            </a>
          ) : (
            <span>{citation.source_name}</span>
          )}
          <code>{citation.source_record_id}</code>
        </div>
      ))}
    </section>
  )
}
export function WikiPage() {
  const [params] = useSearchParams()
  const path = params.get("path") ?? ""
  const query = useInfiniteQuery({
    queryKey: ["wiki-page", path],
    queryFn: ({ pageParam, signal }) =>
      readWiki(path, pageParam.cursor, pageParam.commit, signal),
    initialPageParam: { cursor: 0, commit: "" },
    getNextPageParam: (last) =>
      last.next_cursor == null
        ? undefined
        : { cursor: last.next_cursor, commit: last.commit_sha },
    enabled: !!path,
  })
  return (
    <div className="workspace-page document-page">
      <Link className="knowledge-review-back" to="/knowledge">
        <ArrowLeft size={15} /> Team knowledge
      </Link>
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">PUBLISHED WIKI</span>
          <h1>{query.data?.pages[0]?.title || path || "Select a wiki page"}</h1>
          <p>{path}</p>
        </div>
      </header>
      {query.isPending && !!path && (
        <p className="loading-state">
          <LoaderCircle className="spin" size={18} /> Loading…
        </p>
      )}
      {query.error && (
        <p className="runtime-error" role="alert">
          {errorMessage(query.error)}{" "}
          <button onClick={() => void query.refetch()} type="button">
            Reload snapshot
          </button>
        </p>
      )}
      {query.data?.pages[0]?.stale && (
        <p className="document-help">
          Showing the last indexed snapshot while refresh is pending.
        </p>
      )}
      <section className="panel wiki-reader">
        <Markdown
          path={path}
          content={
            query.data?.pages.map((part) => part.page.content).join("") ?? ""
          }
        />
        <EvidenceLinks
          citations={Object.assign(
            {},
            ...(query.data?.pages.map((part) => part.citations) ?? []),
          )}
        />
        {query.hasNextPage && (
          <button
            className="secondary-button"
            disabled={query.isFetching}
            onClick={() => void query.fetchNextPage()}
            type="button"
          >
            Load more
          </button>
        )}
      </section>
    </div>
  )
}
