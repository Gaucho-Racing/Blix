import axios from "axios"
import { api } from "@/lib/api"
import type { WikiChangeset } from "@/lib/knowledge"

export type DocumentReviewStatus =
  "pending" | "in_review" | "ingested" | "rejected"

export interface DocumentStage {
  name:
    | "uploading"
    | "extracting"
    | "indexing"
    | "analyzing_visuals"
    | "identifying_metadata"
  status: "waiting" | "running" | "completed" | "failed" | "cancelled"
  generation: number
  started_at: string | null
  completed_at: string | null
  updated_at: string
}

export interface TeamDocument {
  stages: DocumentStage[]
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
    | "indexing"
    | "analyzing_visuals"
    | "identifying_metadata"
    | "ready"
    | "partial"
    | "no_changes"
    | "failed"
    | "cancelling"
    | "cancelled"
    | "imported"
  objective: string
  generation: number
  run_id: string
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
  search_index: DocumentSearchIndex
  ready_for_questions: boolean
  review_blocked: boolean
  chunks: DocumentChunk[]
  notes: { revision: number; notes: string; created_at: string } | null
  changesets: WikiChangeset[]
  document: TeamDocument
  pages: {
    page_number: number
    source_record_id: string
    visual_source_record_id?: string
    character_count: number
    image_sha256?: string
    visual_analyzed_at?: string | null
  }[]
}

export interface DocumentSearchIndex {
  status: "waiting" | "running" | "ready" | "retry" | "disabled" | "excluded"
  model: string
  dimensions: number
  namespace: string
  total: number
  ready: number
  pending: number
  processing: number
  retrying: number
  text_chunks: number
  visual_chunks: number
  source_records: number
  indexed_records: number
}

export interface DocumentIndexChunk {
  source_chunk_id: string
  source_record_id: string
  pdf_page: number
  kind: "text" | "visual"
  ordinal: number
  chunker_version: number
  start_offset: number
  end_offset: number
  content: string
  content_hash: string
  rule_ids: string[]
  indexed_at: string | null
  embedding_status: "pending" | "processing" | "ready" | "retry" | "disabled"
  embedding_attempts: number
  embedding_updated_at: string | null
  embedding_available_at: string | null
  embedding_error: string
}

export interface DocumentIndexDetails {
  search_index: DocumentSearchIndex
  chunks: DocumentIndexChunk[]
  offset: number
  next_offset: number | null
}

export async function getDocumentIndexes(id: string, offset: number, signal?: AbortSignal) {
  return (await api.get<DocumentIndexDetails>(`${root}/documents/${id}/indexes`, {
    params: { offset, limit: 25 }, signal,
  })).data
}

export function documentEmbeddingsPending(details: DocumentDetails) {
  return ["waiting", "running", "retry"].includes(details.search_index?.status)
}

export function documentEmbeddingStep(index: DocumentSearchIndex): { status: DocumentStage["status"]; detail: string } {
  const count = `${index.ready} / ${index.total} chunks ready`
  switch (index.status) {
    case "ready": return { status: "completed", detail: count }
    case "disabled": return { status: "cancelled", detail: "Not configured · keyword search available" }
    case "excluded": return { status: "cancelled", detail: `${count} · excluded from search` }
    case "retry": return { status: "running", detail: `${count} · retrying ${index.retrying}` }
    case "running": return { status: "running", detail: count }
    case "waiting": return { status: "waiting", detail: "Waiting for text chunks" }
  }
}
export interface DocumentPage {
  visual_record?: DocumentPage
  page_image?: { document_id: string; page: number; sha256: string }
  source_record_id: string
  source_id: string
  record_type: string
  evidence_type: string
  capture_method: string
  content: string
  metadata: { page_number: number; section_ids?: string[] }
  occurred_at: string
  ingested_at: string
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
  [
    "queued",
    "extracting",
    "indexing",
    "analyzing_visuals",
    "identifying_metadata",
    "cancelling",
  ].includes(status)
export const documentStatusLabel = (status: TeamDocument["status"]) =>
  ({
    uploading: "Upload pending",
    upload_failed: "Upload failed",
    queued: "Queued",
    extracting: "Extracting document",
    indexing: "Making document searchable",
    analyzing_visuals: "Extracting visual evidence",
    identifying_metadata: "Identifying document details",
    ready: "Processing complete",
    partial: "Processing complete · coverage gaps",
    no_changes: "Processing complete",
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
export async function getDocumentPageImage(id: string, page: number, signal?: AbortSignal) {
  return (await api.get<Blob>(`${root}/documents/${id}/pages/${page}/image`, {
    responseType: "blob", timeout: 30_000, signal,
  })).data
}
export async function getDocumentFile(id: string, signal?: AbortSignal) {
  return (
    await api.get<Blob>(`${root}/documents/${id}/file`, {
      responseType: "blob",
      timeout: 30_000,
      signal,
    })
  ).data
}
export async function downloadDocument(document: TeamDocument) {
  const blob = await getDocumentFile(document.document_id)
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
  ({
    pending: "Pending",
    in_review: "In Review",
    ingested: "Ingested",
    rejected: "Rejected",
  })[status]

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

export const documentIngestionSteps: { name: DocumentStage["name"]; label: string }[] = [
  { name: "uploading", label: "Upload document" },
  { name: "extracting", label: "Extract document" },
  { name: "indexing", label: "Make searchable" },
  { name: "analyzing_visuals", label: "Extract visual evidence" },
  { name: "identifying_metadata", label: "Identify document details" },
]

export function documentIngestionProgress({ document, pages, search_index }: DocumentDetails) {
  const finished = documentProcessingComplete(document.status)
  const visualPages = pages.filter((page) => page.image_sha256)
  const analyzed = visualPages.filter((page) => page.visual_analyzed_at).length
  let value = documentIngestionSteps.reduce((total, { name }) => {
    const stage = document.stages.find((stage) => stage.name === name)
    if (finished || stage?.status === "completed") return total + 1
    if (name === "analyzing_visuals" && visualPages.length) return total + analyzed / visualPages.length
    return total
  }, 0)
  const embeddingsEnabled = search_index && !["disabled", "excluded"].includes(search_index.status)
  const max = documentIngestionSteps.length + (embeddingsEnabled ? 1 : 0)
  if (embeddingsEnabled) value += search_index.status === "ready" ? 1 : search_index.total ? Math.min(search_index.ready / search_index.total, .99) : 0
  return {
    value,
    max,
    label: document.status === "analyzing_visuals"
      ? `${analyzed} / ${visualPages.length} page images analyzed`
      : `${Math.floor(value)} / ${max} steps completed`,
  }
}
