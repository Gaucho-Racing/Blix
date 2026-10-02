import { useMutation, useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  BookOpen,
  FileText,
  LoaderCircle,
  Search,
  Upload,
} from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { useAuth } from "@/lib/auth"
import { ProgressBar } from "@/components/progress-bar"
import {
  importDocument,
  listDocuments,
  searchWiki,
  documentStatusLabel,
  validateDocumentFile,
} from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function KnowledgePage() {
  const auth = useAuth()
  const navigate = useNavigate()
  const [query, setQuery] = useState("")
  const [search, setSearch] = useState("")
  const picker = useRef<HTMLInputElement>(null)
  const uploadBusy = useRef(false)
  const [dragging, setDragging] = useState(false)
  const [uploadName, setUploadName] = useState("")
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
    mutationFn: (file: File) => importDocument(file, setProgress),
    onSettled: () => {
      uploadBusy.current = false
    },
    onSuccess: (data) =>
      navigate(`/knowledge/documents/${data.document.document_id}`),
  })
  const { mutate: uploadFile } = upload
  const importFile = useCallback(
    (file: File) => {
      if (!allowed) return
      if (uploadBusy.current) {
        setFileError(
          "An upload is in progress. Wait for it to finish before importing another PDF.",
        )
        return
      }
      try {
        validateDocumentFile(file)
      } catch (error) {
        setFileError(errorMessage(error))
        return
      }
      uploadBusy.current = true
      setFileError("")
      setProgress(0)
      setUploadName(file.name)
      uploadFile(file)
    },
    [allowed, uploadFile],
  )
  useEffect(() => {
    let depth = 0
    const hasFiles = (event: DragEvent) =>
      event.dataTransfer?.types.includes("Files")
    const enter = (event: DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      depth++
      if (allowed) setDragging(true)
    }
    const over = (event: DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer)
        event.dataTransfer.dropEffect =
          allowed && !uploadBusy.current ? "copy" : "none"
    }
    const leave = (event: DragEvent) => {
      if (!hasFiles(event)) return
      depth = Math.max(0, depth - 1)
      if (depth === 0) setDragging(false)
    }
    const drop = (event: DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      depth = 0
      setDragging(false)
      if (!allowed) return
      const files = event.dataTransfer?.files
      if (!files?.length) return
      if (files.length !== 1) {
        setFileError("Import one PDF at a time.")
        return
      }
      importFile(files[0])
    }
    window.addEventListener("dragenter", enter)
    window.addEventListener("dragover", over)
    window.addEventListener("dragleave", leave)
    window.addEventListener("drop", drop)
    return () => {
      window.removeEventListener("dragenter", enter)
      window.removeEventListener("dragover", over)
      window.removeEventListener("dragleave", leave)
      window.removeEventListener("drop", drop)
    }
  }, [allowed, importFile])
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
          disabled={!allowed || upload.isPending}
          type="button"
          onClick={() => picker.current?.click()}
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
      <input
        ref={picker}
        accept="application/pdf,.pdf"
        aria-label="Choose PDF to import"
        hidden
        type="file"
        onChange={(event) => {
          const file = event.currentTarget.files?.[0]
          event.currentTarget.value = ""
          if (file) importFile(file)
        }}
      />
      {dragging && <div className="document-drop-overlay" aria-hidden="true" />}
      {allowed && (
        <button
          className={`document-import-target ${dragging ? "is-dragging" : ""}`}
          disabled={upload.isPending}
          onClick={() => picker.current?.click()}
          type="button"
        >
          <span className="document-import-icon">
            <Upload size={24} />
          </span>
          <span>
            <strong>
              {dragging ? "Drop your PDF here" : "Drop a PDF anywhere"}
            </strong>
            <small>
              {dragging
                ? "Ready to import"
                : "Or click to choose a file. Blix will identify the details."}
            </small>
          </span>
        </button>
      )}
      {(fileError || upload.error) && (
        <p className="runtime-error" role="alert">
          {fileError || errorMessage(upload.error)}
        </p>
      )}
      {upload.isPending && (
        <section className="panel document-upload-status" aria-live="polite">
          <LoaderCircle className="spin" size={20} />
          <div>
            <strong>{uploadName}</strong>
            <p>
              {progress < 100
                ? `Uploading ${progress}%`
                : "Starting background ingestion…"}
            </p>
            <ProgressBar value={progress} label="PDF upload progress" />
          </div>
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
          <p className="document-help">
            Your imported documents and wiki drafts will appear here.
          </p>
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
                {[document.publisher, document.version]
                  .filter(Boolean)
                  .join(" · ") ||
                  "Document details will be identified during ingestion"}
              </p>
              <footer>
                {documentStatusLabel(document.status)} ·{" "}
                {document.page_count || "—"} pages
                <span>{document.published_at?.slice(0, 10)}</span>
              </footer>
            </Link>
          ))}
        </div>
      </section>
    </div>
  )
}
