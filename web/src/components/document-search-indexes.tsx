import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, ArrowRight, CircleCheck, LoaderCircle } from "lucide-react"
import { useState } from "react"
import { getDocumentIndexes, documentEmbeddingsPending, type DocumentDetails, type DocumentIndexChunk } from "@/lib/documents"
import { formatTimestamp } from "@/lib/format"
import { errorMessage } from "@/lib/runtime"

type IndexView = "chunks" | "embeddings"
const embeddingLabels: Record<DocumentIndexChunk["embedding_status"], string> = {
  pending: "Pending", processing: "Generating", ready: "Ready", retry: "Retry scheduled", disabled: "Not configured",
}

export function DocumentSearchIndexes({ details, view, onSource }: {
  details: DocumentDetails
  view: IndexView
  onSource: (page: number, kind: "text" | "visual") => void
}) {
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<string | null>(null)
  const query = useQuery({
    queryKey: ["document-indexes", details.document.document_id, offset],
    queryFn: ({ signal }) => getDocumentIndexes(details.document.document_id, offset, signal),
    refetchInterval: (query) => documentEmbeddingsPending(details) ||
      ["waiting", "running", "retry"].includes(query.state.data?.search_index.status ?? "") ||
      details.search_index?.indexed_records < details.search_index?.source_records ? 2000 : false,
  })
  const summary = query.data?.search_index ?? details.search_index
  const chunks = query.data?.chunks ?? []
  const chunk = chunks.find((item) => item.source_chunk_id === selected) ?? chunks[0]
  return (
    <div className="document-index-view">
      {summary && <div className="document-index-summary">
        <span>{summary.text_chunks} page-text chunks · {summary.visual_chunks} visual-description chunks</span>
        <span>{view === "embeddings"
          ? summary.status === "disabled" ? "Embeddings are not configured." : `${summary.ready} / ${summary.total} chunks ready · ${summary.model} · ${summary.dimensions} dimensions`
          : `${summary.indexed_records} / ${summary.source_records} source records indexed`}</span>
      </div>}
      {query.isPending ? <p className="loading-state"><LoaderCircle className="spin" size={16} /> Loading search indexes…</p>
        : query.isError ? <p className="runtime-error" role="alert">{errorMessage(query.error)} <button type="button" onClick={() => void query.refetch()}>Retry</button></p>
        : !chunk ? <p className="document-source-empty">Search chunks will appear as source records are indexed.</p>
        : <>
          <div className="document-source-workspace">
            <ul className="document-source-record-list" aria-label="Search chunks">
              {chunks.map((item) => <li key={item.source_chunk_id}>
                <button type="button" aria-pressed={chunk.source_chunk_id === item.source_chunk_id} onClick={() => setSelected(item.source_chunk_id)}>
                  <strong>PDF page {item.pdf_page} · Chunk {item.ordinal}</strong>
                  <span>{item.kind === "text" ? "Page text" : "Visual description"}{view === "embeddings" && ` · ${embeddingLabels[item.embedding_status]}`}</span>
                </button>
              </li>)}
            </ul>
            <div className="document-source-detail">
              <header className="document-source-navigation">
                <div className="document-source-record-heading">
                  <span className="workspace-kicker">{view === "embeddings" ? "EMBEDDING" : "TEXT CHUNK"}</span>
                  <h3>PDF page {chunk.pdf_page} · Chunk {chunk.ordinal}</h3>
                  <code>{chunk.source_chunk_id}</code>
                </div>
                <button className="secondary-button" type="button" onClick={() => onSource(chunk.pdf_page, chunk.kind)}>View source record <ArrowRight size={15} /></button>
              </header>
              <dl className="document-source-provenance">
                <div><dt>Source record</dt><dd><code>{chunk.source_record_id}</code></dd></div>
                <div><dt>Evidence</dt><dd>{chunk.kind === "text" ? "Extracted page text" : "Visual description"}</dd></div>
                <div><dt>Text range</dt><dd>Bytes {chunk.start_offset}–{chunk.end_offset}</dd></div>
                <div><dt>Text index</dt><dd>Chunker v{chunk.chunker_version}{chunk.indexed_at && ` · ${formatTimestamp(chunk.indexed_at)}`}</dd></div>
                {view === "embeddings" && <>
                  <div><dt>Embedding status</dt><dd className={`document-embedding-state embedding-${chunk.embedding_status}`}>
                    {chunk.embedding_status === "ready" && <CircleCheck size={15} />}
                    {chunk.embedding_status === "processing" && <LoaderCircle className="spin" size={15} />}
                    {embeddingLabels[chunk.embedding_status]}
                  </dd></div>
                  <div><dt>Generation attempts</dt><dd>{chunk.embedding_attempts}{chunk.embedding_updated_at && ` · ${formatTimestamp(chunk.embedding_updated_at)}`}</dd></div>
                  {chunk.embedding_status === "retry" && chunk.embedding_available_at && <div><dt>Next retry</dt><dd>{formatTimestamp(chunk.embedding_available_at)}</dd></div>}
                </>}
              </dl>
              {view === "embeddings" && <details className="document-index-identifiers"><summary>Embedding identity</summary><dl className="document-source-provenance">
                <div><dt>Content hash</dt><dd><code>{chunk.content_hash}</code></dd></div>
                <div><dt>Model namespace</dt><dd><code>{summary?.namespace || "Not configured"}</code></dd></div>
              </dl></details>}
              {chunk.embedding_error && view === "embeddings" && <p className="runtime-error">{chunk.embedding_error}</p>}
              {!!chunk.rule_ids?.length && <div className="document-visual-sections" aria-label="Indexed rule identifiers">{chunk.rule_ids.map((id) => <span key={id}>{id}</span>)}</div>}
              <div className="document-source-content"><pre aria-label="Indexed chunk text">{chunk.content}</pre></div>
            </div>
          </div>
          <div className="document-index-pagination">
            <span>Chunks {offset + 1}–{offset + chunks.length} of {summary?.total}</span>
            <div className="document-source-buttons">
              <button className="secondary-button" type="button" disabled={offset === 0} onClick={() => { setOffset(Math.max(0, offset - 25)); setSelected(null) }}><ArrowLeft size={15} /> Previous</button>
              <button className="secondary-button" type="button" disabled={query.data?.next_offset == null} onClick={() => { setOffset(query.data!.next_offset!); setSelected(null) }}>Next <ArrowRight size={15} /></button>
            </div>
          </div>
        </>}
    </div>
  )
}
