import { useMutation, useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  BookOpen,
  FileText,
  LoaderCircle,
  Search,
  Upload,
} from "lucide-react"
import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { useAuth } from "@/lib/auth"
import {
  importDocument,
  listDocuments,
  searchWiki,
  documentStatusLabel,
} from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function KnowledgePage() {
  const auth = useAuth()
  const navigate = useNavigate()
  const [query, setQuery] = useState("")
  const [search, setSearch] = useState("")
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState(0)
  const [fileError, setFileError] = useState("")
  const allowed =
    auth.isAuthenticated && (auth.user?.groups.includes("BlixUsers") ?? false)
  const documents = useQuery({
    queryKey: ["documents"],
    queryFn: listDocuments,
    enabled: allowed,
    refetchInterval: 5000,
  })
  const results = useQuery({
    queryKey: ["wiki-search", search],
    queryFn: ({ signal }) => searchWiki(search, signal),
    enabled: allowed && !!search,
  })
  const upload = useMutation({
    mutationFn: (form: FormData) => importDocument(form, setProgress),
    onSuccess: (data) =>
      navigate(`/knowledge/documents/${data.document.document_id}`),
  })
  return (
    <div className="workspace-page knowledge-page">
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">TEAM KNOWLEDGE</span>
          <h1>Evidence into understanding.</h1>
          <p>
            Search the published wiki. Import documents and let Blix draft
            grounded updates.
          </p>
        </div>
        <button
          className="primary-button"
          disabled={!allowed}
          type="button"
          onClick={() => setUploading(!uploading)}
        >
          <Upload size={16} /> Import PDF
        </button>
      </header>
      {!allowed && !auth.isLoading && (
        <div className="knowledge-review-notice">
          <BookOpen size={20} />
          <p>
            {auth.isAuthenticated ? (
              "BlixUsers membership is required to access team knowledge."
            ) : (
              <Link to="/auth/login">Sign in to access team knowledge.</Link>
            )}
          </p>
        </div>
      )}
      {uploading && (
        <section className="panel document-import">
          <div className="content-heading">
            <div>
              <h2>Import a document</h2>
              <span>
                The original PDF and its page-level evidence are shared with the
                team.
              </span>
            </div>
          </div>
          <form
            className="document-form"
            onSubmit={(event) => {
              event.preventDefault()
              const form = new FormData(event.currentTarget)
              const file = form.get("file")
              if (
                !(file instanceof File) ||
                !file.size ||
                file.size > 20 * 1024 * 1024
              ) {
                setFileError("Choose a PDF up to 20 MiB.")
                return
              }
              setFileError("")
              setProgress(0)
              upload.mutate(form)
            }}
          >
            <fieldset disabled={upload.isPending}>
              <label className="document-file">
                PDF file
                <input
                  accept="application/pdf,.pdf"
                  name="file"
                  required
                  type="file"
                />
                <small>
                  Selectable text required · up to 20 MiB · up to 500 pages
                </small>
              </label>
              <div className="document-form-grid">
                <label>
                  Document title
                  <input
                    maxLength={512}
                    name="title"
                    placeholder="Formula SAE Rules 2027"
                    required
                  />
                </label>
                <label>
                  Publisher
                  <input
                    maxLength={512}
                    name="publisher"
                    placeholder="SAE International"
                    required
                  />
                </label>
                <label>
                  Version
                  <input
                    maxLength={512}
                    name="version"
                    placeholder="1.0"
                    required
                  />
                </label>
                <label>
                  Publication / revision date
                  <input name="published_at" required type="date" />
                </label>
                <label>
                  Document type
                  <select defaultValue="reference" name="kind">
                    <option value="rules">Rules / requirements</option>
                    <option value="roadmap">Roadmap / future guidance</option>
                    <option value="reference">Reference material</option>
                  </select>
                </label>
                <label>
                  Official source URL (optional)
                  <input
                    name="external_url"
                    placeholder="https://…"
                    type="url"
                  />
                </label>
              </div>
              <label>
                What should Blix learn? (optional)
                <textarea
                  name="objective"
                  rows={3}
                  maxLength={4000}
                  placeholder="Build a useful rules reference with exact rule identifiers and exceptions."
                />
              </label>
              <p className="document-help">
                Use the date and version printed in the document. Import starts
                background reading and a wiki draft for review. You can leave
                the page while Blix works.
              </p>
              {(upload.error || fileError) && (
                <p className="runtime-error" role="alert">
                  {fileError || errorMessage(upload.error)}
                </p>
              )}
              {upload.isPending && (
                <div className="document-progress">
                  <progress max={100} value={progress} />
                  <span>
                    {progress < 100
                      ? `Uploading ${progress}%`
                      : "Queuing background ingestion…"}
                  </span>
                </div>
              )}
              <button className="primary-button" type="submit">
                {upload.isPending ? (
                  <LoaderCircle className="spin" size={16} />
                ) : (
                  <Upload size={16} />
                )}
                {upload.isPending ? "Importing…" : "Upload & start ingestion"}
              </button>
            </fieldset>
          </form>
        </section>
      )}
      <section className="knowledge-section">
        <div className="content-heading">
          <div>
            <h2>Published wiki</h2>
            <span>Search the indexed team knowledge.</span>
          </div>
        </div>
        <form
          className="knowledge-search"
          onSubmit={(event) => {
            event.preventDefault()
            setSearch(query.trim())
          }}
        >
          <Search size={20} />
          <input
            aria-label="Search wiki"
            disabled={!allowed}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Brake requirements, accumulator, GR26…"
            value={query}
            type="search"
          />
          <button type="submit" disabled={!query.trim() || !allowed}>
            Search
          </button>
        </form>
        {!!search && (
          <div className="wiki-results">
            {results.isPending && (
              <p className="loading-state">
                <LoaderCircle className="spin" size={16} /> Searching…
              </p>
            )}
            {results.error && (
              <p role="alert" className="runtime-error">
                {errorMessage(results.error)}
              </p>
            )}
            {results.data?.stale && (
              <p className="document-help">
                Showing the last indexed snapshot. Refresh is pending.
              </p>
            )}
            {results.data && !results.data.results.length && (
              <p className="document-help">
                No matching wiki sections. Try different terms or ask Blix to
                research this.
              </p>
            )}
            {results.data?.results.map((hit) => (
              <Link
                key={`${hit.path}:${hit.section}`}
                className="wiki-result"
                to={`/knowledge/page?path=${encodeURIComponent(hit.path)}`}
              >
                <div>
                  <BookOpen size={17} />
                  <strong>{hit.title || hit.path}</strong>
                  <span>{hit.season}</span>
                </div>
                <small>{hit.heading || hit.path}</small>
                <p>{hit.content.slice(0, 400)}</p>
              </Link>
            ))}
          </div>
        )}
      </section>
      <section className="knowledge-section">
        <div className="content-heading">
          <div>
            <h2>Document library</h2>
            <span>
              Original evidence, ingestion progress, and wiki proposals.
            </span>
          </div>
        </div>
        {documents.isLoading && (
          <p className="loading-state">Loading documents…</p>
        )}
        {documents.error && (
          <p role="alert" className="runtime-error">
            {errorMessage(documents.error)}{" "}
            <button onClick={() => void documents.refetch()} type="button">
              Retry
            </button>
          </p>
        )}
        {allowed && documents.data?.length === 0 && (
          <div className="document-empty">
            <FileText size={30} />
            <h3>Your first source starts here.</h3>
            <p>
              Import official rules, design references, or team reports to build
              a wiki grounded in evidence.
            </p>
            <button
              className="secondary-button"
              onClick={() => setUploading(true)}
              type="button"
            >
              Import a PDF
            </button>
          </div>
        )}
        <div className="document-grid">
          {documents.data?.map((document) => (
            <Link
              className="document-card panel"
              key={document.document_id}
              to={`/knowledge/documents/${document.document_id}`}
            >
              <div>
                <span className="document-type">{document.kind}</span>
                <ArrowRight size={16} />
              </div>
              <FileText size={24} />
              <h3>{document.title}</h3>
              <p>
                {document.publisher} · {document.version}
              </p>
              <footer>
                {documentStatusLabel(document.status)} ·{" "}
                {document.page_count || "—"} pages
                <span>{document.published_at.slice(0, 10)}</span>
              </footer>
            </Link>
          ))}
        </div>
      </section>
    </div>
  )
}
