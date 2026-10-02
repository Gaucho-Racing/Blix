import axios from "axios"
import { api } from "@/lib/api"
import type { WikiChangeset } from "@/lib/knowledge"

export type DocumentReviewStatus = "in_review" | "ingested" | "rejected"

export interface TeamDocument {
  review_status: DocumentReviewStatus
  reviewed_by: string
  review_note: string
  reviewed_at?: string
  document_id: string
  uploaded_by: string
  depot_file_id: string
  status:
    | "uploading"
    | "upload_failed"
    | "queued"
    | "extracting"
    | "reading"
    | "reconciling"
    | "ready"
    | "partial"
    | "no_changes"
    | "failed"
    | "cancelling"
    | "cancelled"
    | "imported"
  objective: string
  generation: number
  conversation_id: string
  task_id: string
  error: string
  title: string
  filename: string
  publisher: string
  version: string
  tags: string[]
  published_at: string | null
  extracted_metadata: ExtractedDocumentMetadata | null
  metadata_overrides: Record<string, boolean> | null
  metadata_revision: number
  external_url: string
  page_count: number
  size_bytes: number
  created_at: string
}
export interface ExtractedDocumentMetadata {
  title: string
  publisher: string
  version: string
  tags: string[]
  published_at: string
  external_url: string
  source_record_ids: string[]
  warnings: string[]
}
export interface DocumentMetadataFields {
  title: string
  publisher: string
  version: string
  tags: string[]
  published_at: string
  external_url: string
}
export interface DocumentChunk {
  number: number
  first_page: number
  last_page: number
  completed_at: string | null
  findings: string
}
export interface DocumentDetails {
  review_blocked: boolean
  chunks: DocumentChunk[]
  notes: { revision: number; notes: string; created_at: string } | null
  changesets: WikiChangeset[]
  document: TeamDocument
  pages: {
    page_number: number
    source_record_id: string
    character_count: number
  }[]
}
export interface DocumentPage {
  source_record_id: string
  content: string
  metadata: { page_number: number }
  occurred_at: string
}
export interface WikiHit {
  path: string
  title: string
  heading: string
  content: string
  season?: string
  section: number
}
export interface WikiSearch {
  results: WikiHit[]
  stale: boolean
  indexed_at: string
  commit_sha: string
}
export interface DocumentCitation {
  source_record_id: string
  source_name: string
  document_id?: string
  pdf_page?: number
  external_url: string
}
export interface WikiPage {
  citations: Record<string, DocumentCitation>
  page: { path: string; content: string }
  title: string
  stale: boolean
  commit_sha: string
  next_cursor?: number
  total_bytes: number
}
const root = "/knowledge/v1"
export async function listDocuments() {
  return (await api.get<{ documents: TeamDocument[] }>(`${root}/documents`))
    .data.documents
}
export function validateDocumentFile(file: File) {
  if (!file.size || file.size > 20 * 1024 * 1024 || !/\.pdf$/i.test(file.name))
    throw new Error("Choose a PDF up to 20 MiB.")
}
export async function importDocument(
  file: File,
  progress: (percent: number) => void,
) {
  validateDocumentFile(file)
  const intent = (
    await api.post<{
      document: TeamDocument
      upload: { upload_url: string; method: string }
    }>(`${root}/documents/uploads`, { filename: file.name })
  ).data
  await axios.put(intent.upload.upload_url, file, {
    headers: { "Content-Type": "application/pdf" },
    timeout: 120_000,
    onUploadProgress: (event) =>
      progress(Math.round((event.progress ?? 0) * 100)),
  })
  return (
    await api.post<DocumentDetails>(
      `${root}/documents/${intent.document.document_id}/complete`,
    )
  ).data
}
export async function updateDocumentMetadata(
  id: string,
  revision: number,
  fields: Partial<DocumentMetadataFields>,
) {
  return (
    await api.patch<DocumentDetails>(`${root}/documents/${id}/metadata`, {
      revision,
      ...fields,
    })
  ).data
}
export async function controlDocument(
  id: string,
  operation: "cancel" | "retry" | "complete",
) {
  const path = operation === "complete" ? "complete" : `ingestion/${operation}`
  return (await api.post<DocumentDetails>(`${root}/documents/${id}/${path}`))
    .data
}
export async function getDocumentChunk(
  id: string,
  number: number,
  signal?: AbortSignal,
) {
  return (
    await api.get<{
      chunk: DocumentChunk
      evidence: { page: number; source_record_id: string; content: string }[]
    }>(`${root}/documents/${id}/chunks/${number}`, { signal })
  ).data
}
export const documentProcessing = (status: TeamDocument["status"]) =>
  ["queued", "extracting", "reading", "reconciling", "cancelling"].includes(
    status,
  )
export const documentStatusLabel = (status: TeamDocument["status"]) =>
  ({
    uploading: "Upload pending",
    upload_failed: "Upload failed",
    queued: "Queued",
    extracting: "Extracting text",
    reading: "Reading document",
    reconciling: "Drafting wiki changes",
    ready: "Reading complete",
    partial: "Reading complete · coverage gaps",
    no_changes: "Processed · no new changes",
    failed: "Processing failed",
    cancelling: "Cancelling",
    cancelled: "Cancelled",
    imported: "Source evidence imported",
  })[status]
export async function getDocument(id: string, signal?: AbortSignal) {
  return (await api.get<DocumentDetails>(`${root}/documents/${id}`, { signal }))
    .data
}
export async function getDocumentPage(
  id: string,
  number: number,
  signal?: AbortSignal,
) {
  return (
    await api.get<DocumentPage>(`${root}/documents/${id}/pages/${number}`, {
      signal,
    })
  ).data
}
export async function downloadDocument(document: TeamDocument) {
  const blob = (
    await api.get<Blob>(`${root}/documents/${document.document_id}/file`, {
      responseType: "blob",
      timeout: 30_000,
    })
  ).data
  const url = URL.createObjectURL(blob)
  const link = window.document.createElement("a")
  link.href = url
  link.download = document.filename || "document.pdf"
  link.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
}
export async function searchWiki(q: string, signal?: AbortSignal) {
  return (
    await api.get<WikiSearch>(`${root}/wiki/search`, {
      params: { q, limit: 10 },
      signal,
    })
  ).data
}
export async function readWiki(
  path: string,
  cursor = 0,
  commit = "",
  signal?: AbortSignal,
) {
  return (
    await api.get<WikiPage>(`${root}/wiki/page`, {
      params: { path, cursor, commit_sha: commit, max_bytes: 32_000 },
      signal,
    })
  ).data
}

export const documentReviewLabel = (status: DocumentReviewStatus) =>
  ({ in_review: "In Review", ingested: "Ingested", rejected: "Rejected" })[
    status
  ]

export const documentProcessingComplete = (status: TeamDocument["status"]) =>
  ["ready", "partial", "no_changes", "imported"].includes(status)

export async function reviewDocument(
  id: string,
  decision: "approve" | "reject",
  note: string,
) {
  return (
    await api.post<DocumentDetails>(
      `${root}/documents/${encodeURIComponent(id)}/review/${decision}`,
      { note },
    )
  ).data
}
