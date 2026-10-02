import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { isAxiosError } from "axios"
import { ArrowLeft, ArrowRight, Check, FileCode2, GitCommitHorizontal, GitPullRequest, LoaderCircle, RefreshCw, ShieldCheck, X } from "lucide-react"
import { useEffect, useRef, useState, type FormEvent } from "react"
import { Link, useParams, useSearchParams } from "react-router-dom"

import { StatusBadge } from "@/components/status-badge"
import { formatTimestamp } from "@/lib/format"
import { getChangeset, getChangesetBase, listChangesets, reviewChangeset, type WikiChangeset, type WikiChangesetFile } from "@/lib/knowledge"

const filters = [
  ["proposed", "Proposed"], ["", "All changes"], ["committed", "Published"],
  ["rejected", "Rejected"], ["failed", "Failed"], ["prepared", "Prepared"],
] as const

function errorMessage(error: unknown) {
  if (isAxiosError(error)) {
    if (error.response?.status === 403) return "BlixAdmins membership is required to review knowledge changes."
    const message = error.response?.data?.error
    if (typeof message === "string") return message
  }
  return "Could not reach the knowledge service. Refresh to check the latest state before retrying."
}

export function KnowledgeReviewsPage() {
  const [params, setParams] = useSearchParams()
  const requested = params.get("status") ?? "proposed"
  const status = filters.some(([value]) => value === requested) ? requested : "proposed"
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
        <div><div className="eyebrow">ADMIN · KNOWLEDGE</div><h1>Wiki changes</h1><p>Review what Blix has learned before it becomes shared team knowledge.</p></div>
        <button className="secondary-button" disabled={query.isFetching} onClick={() => void query.refetch()} type="button"><RefreshCw className={query.isFetching ? "spin" : ""} size={15} /> Refresh</button>
      </header>
      <div className="knowledge-review-notice"><ShieldCheck size={19} /><div><strong>A second set of eyes</strong><p>Approve to publish the exact proposed files. Reject to keep them out of the wiki. Blix can still publish supported updates directly.</p></div></div>
      <section className="panel knowledge-review-list">
        <div className="knowledge-review-filters" aria-label="Filter changesets">
          {filters.map(([value, label]) => <button aria-pressed={status === value} className={status === value ? "active" : ""} key={value} onClick={() => setParams({ status: value })} type="button">{label}</button>)}
        </div>
        {query.isPending ? <Loading label="Loading wiki changes" /> : <>
          {query.isError && <div className="error-state" role="alert">{errorMessage(query.error)}</div>}
          {!query.isError && changesets.length === 0 && <div className="knowledge-review-empty"><GitPullRequest size={30} /><h2>{status === "proposed" ? "Nothing waiting for review" : "No changes here yet"}</h2><p>Ask Blix to remember something about the team. Proposed wiki updates will appear here.</p></div>}
          {changesets.map((changeset) => <Link className="knowledge-review-row" key={changeset.changeset_id} to={`/admin/knowledge/changesets/${changeset.changeset_id}`}>
            <span className="knowledge-review-row-icon"><GitPullRequest size={19} /></span>
            <div className="knowledge-review-row-copy"><div><strong>{changeset.summary}</strong><StatusBadge status={changeset.status} /></div><code>{changeset.changeset_id}</code><p>{changeset.season_slug || "Season-independent"}<span>·</span>{changeset.type}<span>·</span>{formatTimestamp(changeset.created_at)}</p></div>
            <ArrowRight size={18} />
          </Link>)}
          {query.hasNextPage && <div className="knowledge-review-list-footer"><button className="secondary-button" disabled={query.isFetching} onClick={() => void query.fetchNextPage()} type="button">{query.isFetchingNextPage ? "Loading…" : "Load older changes"}</button></div>}
        </>}
      </section>
    </div>
  )
}

export function KnowledgeReviewDetailsPage() {
  const { changesetID = "" } = useParams()
  const query = useQuery({ queryKey: ["knowledge-changeset", changesetID], queryFn: () => getChangeset(changesetID) })
  return <div className="page knowledge-reviews-page">
    <Link className="knowledge-review-back" to="/admin/knowledge"><ArrowLeft size={15} /> Wiki changes</Link>
    {query.isPending ? <Loading label="Loading proposal" /> : query.isError ? <div className="panel error-state" role="alert">{errorMessage(query.error)} <button className="secondary-button" onClick={() => void query.refetch()} type="button">Retry</button></div> :
      <ChangesetReview changeset={query.data.changeset} files={query.data.files} key={changesetID} onRefresh={() => void query.refetch()} refreshing={query.isFetching} />}
  </div>
}

function ChangesetReview({ changeset, files, onRefresh, refreshing }: { changeset: WikiChangeset; files: WikiChangesetFile[]; onRefresh: () => void; refreshing: boolean }) {
  const [selected, setSelected] = useState(files[0]?.path ?? "")
  const [decision, setDecision] = useState<"approve" | "reject" | null>(null)
  const [note, setNote] = useState("")
  const queryClient = useQueryClient()
  const file = files.find((item) => item.path === selected) ?? files[0]
  const mutation = useMutation({
    mutationFn: ({ action, reviewNote }: { action: "approve" | "reject"; reviewNote: string }) => reviewChangeset(changeset.changeset_id, action, reviewNote),
    onSuccess: (data) => {
      queryClient.setQueryData(["knowledge-changeset", changeset.changeset_id], data)
      setDecision(null)
      setNote("")
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["knowledge-changesets"] }),
        queryClient.invalidateQueries({ queryKey: ["knowledge-changeset", changeset.changeset_id] }),
      ])
    },
  })
  const canApprove = ["proposed", "failed", "prepared"].includes(changeset.status)
  const canReject = ["proposed", "failed"].includes(changeset.status) && !changeset.result_commit_sha
  const retryPublication = Boolean(changeset.result_commit_sha) || changeset.review_decision === "approved"
  const choose = (action: "approve" | "reject") => { mutation.reset(); setDecision(action); setNote("") }

  return <>
    <header className="page-header knowledge-review-heading">
      <div><div className="eyebrow">ADMIN · KNOWLEDGE REVIEW</div><h1>{changeset.summary}</h1><code>{changeset.changeset_id}</code></div>
      <StatusBadge status={changeset.status} />
    </header>
    <section className="panel knowledge-review-context">
      <div><span>Scope</span><strong>{changeset.season_slug || "Season-independent"}</strong></div>
      <div><span>Change type</span><strong>{changeset.type} · {files.length} {files.length === 1 ? "file" : "files"}</strong></div>
      <div><span>Created</span><strong>{formatTimestamp(changeset.created_at)}</strong></div>
      <div><span>Origin</span><strong>{changeset.origin}</strong></div>
      {changeset.conversation_id && <div className="knowledge-review-provenance"><span>Source conversation / session</span><Link to={`/chat/${changeset.conversation_id}`}>{changeset.conversation_id}</Link><code>{changeset.session_id}</code></div>}
      <div className="knowledge-review-provenance"><span>Base revision</span><code>{changeset.base_commit_sha || "Empty repository"}</code></div>
    </section>
    {changeset.error && <div className="knowledge-review-warning" role="alert"><strong>Publication did not complete</strong><p>{changeset.error}</p><small>Revision conflicts require a fresh proposal from Blix. No force-publish is performed.</small></div>}
    {changeset.reviewed_by && <section className="panel knowledge-review-audit"><ShieldCheck size={19} /><div><strong>{changeset.review_decision === "rejected" ? "Rejected" : "Approved for publication"}{changeset.reviewed_at && ` · ${formatTimestamp(changeset.reviewed_at)}`}</strong><code>{changeset.reviewed_by}</code>{changeset.review_note && <p>{changeset.review_note}</p>}</div></section>}
    {changeset.status === "committed" && <div className="knowledge-review-published"><GitCommitHorizontal size={19} /><div><strong>Published to the wiki{changeset.committed_at && ` · ${formatTimestamp(changeset.committed_at)}`}</strong><code>{changeset.result_commit_sha}</code></div></div>}
    <section className="panel knowledge-review-files">
      <div className="panel-toolbar"><div><h2>Proposed files</h2><span>Compare with the exact base revision, not the current wiki.</span></div><button className="secondary-button" disabled={refreshing || mutation.isPending} onClick={onRefresh} type="button"><RefreshCw className={refreshing ? "spin" : ""} size={14} /> Refresh</button></div>
      <div className="knowledge-review-file-tabs" aria-label="Changed files">{files.map((item) => <button aria-pressed={file?.path === item.path} className={file?.path === item.path ? "active" : ""} key={item.path} onClick={() => setSelected(item.path)} type="button"><FileCode2 size={14} /><span>{item.path}</span><small>{item.operation === "delete" ? "delete" : "upsert"}</small></button>)}</div>
      {file && <FileComparison changesetID={changeset.changeset_id} file={file} key={file.path} />}
    </section>
    {canApprove && <section className="panel knowledge-review-actions">
      <div><strong>{retryPublication ? "Reconcile publication" : "Ready for a decision?"}</strong><p>{canReject ? "Approval publishes these files to GitHub. Rejection preserves the proposal and its source evidence." : "Publication has started. Retry safely to resolve its outcome; rejection is no longer available."}</p></div>
      <div>{canReject && <button className="secondary-button knowledge-reject-button" disabled={mutation.isPending} onClick={() => choose("reject")} type="button"><X size={15} /> Reject</button>}<button className="primary-button" disabled={mutation.isPending} onClick={() => choose("approve")} type="button"><Check size={15} /> {retryPublication ? "Retry publication" : "Approve & publish"}</button></div>
    </section>}
    {decision && <ReviewDialog decision={decision} note={note} setNote={setNote} pending={mutation.isPending}
      allowed={decision === "approve" ? canApprove : canReject} error={mutation.isError ? errorMessage(mutation.error) : null}
      fileCount={files.length} onClose={() => { if (!mutation.isPending) setDecision(null) }}
      onSubmit={() => mutation.mutate({ action: decision, reviewNote: note })} />}
  </>
}

function FileComparison({ changesetID, file }: { changesetID: string; file: WikiChangesetFile }) {
  const query = useQuery({ queryKey: ["knowledge-base", changesetID, file.path], queryFn: () => getChangesetBase(changesetID, file.path), staleTime: Infinity })
  if (query.isPending) return <Loading label="Loading base revision" />
  if (query.isError) return <div className="error-state" role="alert">Cannot load the original file. {errorMessage(query.error)} <button className="secondary-button" onClick={() => void query.refetch()} type="button">Retry</button></div>
  return <div className="knowledge-file-comparison">
    <section><header><span>Before</span><small>{query.data.exists ? "Base revision" : "New file"}</small></header>{query.data.exists ? <pre>{query.data.content || "(empty file)"}</pre> : <div className="knowledge-file-absent">This file does not exist in the base revision.</div>}</section>
    <section className={file.operation === "delete" ? "is-deleted" : "is-proposed"}><header><span>{file.operation === "delete" ? "Deleted" : "After"}</span><small>Proposed change</small></header>{file.operation === "delete" ? <div className="knowledge-file-absent">This file will be removed from the wiki.</div> : <pre>{file.content || "(empty file)"}</pre>}</section>
  </div>
}

function Loading({ label }: { label: string }) { return <div className="loading-state"><LoaderCircle className="spin" size={18} /> {label}</div> }

function ReviewDialog({ decision, note, setNote, pending, allowed, error, fileCount, onClose, onSubmit }: {
  decision: "approve" | "reject"; note: string; setNote: (value: string) => void; pending: boolean; allowed: boolean;
  error: string | null; fileCount: number; onClose: () => void; onSubmit: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close() }, [])
  const reject = decision === "reject"
  const submit = (event: FormEvent) => { event.preventDefault(); if (!pending && allowed) onSubmit() }
  return <dialog aria-labelledby="review-dialog-title" className="knowledge-review-dialog" onCancel={(event) => { event.preventDefault(); onClose() }} ref={ref}>
    <form onSubmit={submit}>
      <div className="eyebrow">WIKI REVIEW</div><h2 id="review-dialog-title">{reject ? "Reject this proposal?" : "Publish these changes?"}</h2>
      <p>{reject ? "The proposal and source evidence will be retained, but this changeset can no longer be published." : `This will publish ${fileCount} ${fileCount === 1 ? "file change" : "file changes"} to the wiki. Existing files may be replaced or deleted. The reviewer identity and time will be recorded.`}</p>
      <label htmlFor="review-note">{reject ? "Reason for rejection" : "Review note (optional)"}</label>
      <textarea disabled={pending} id="review-note" maxLength={2000} onChange={(event) => setNote(event.target.value)} placeholder={reject ? "What should Blix reconsider?" : "Anything future reviewers should know?"} required={reject} rows={4} value={note} />
      {error && <div className="error-state" role="alert">{error}</div>}
      <footer><button className="secondary-button" disabled={pending} onClick={onClose} type="button">Cancel</button><button className={`primary-button${reject ? " knowledge-reject-confirm" : ""}`} disabled={pending || !allowed || (reject && !note.trim())} type="submit">{pending && <LoaderCircle className="spin" size={15} />}{pending ? "Saving decision…" : reject ? "Reject proposal" : "Approve & publish"}</button></footer>
    </form>
  </dialog>
}
