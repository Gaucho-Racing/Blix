import { api } from "@/lib/api"
import type { WikiChangesetDetails } from "@/lib/knowledge"

export interface TeamDocument {
  document_id: string
  title: string
  filename: string
  publisher: string
  version: string
  kind: "rules" | "roadmap" | "reference"
  published_at: string
  external_url: string
  page_count: number
  size_bytes: number
  created_at: string
}
export interface DocumentDetails {
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
export async function importDocument(
  form: FormData,
  progress: (percent: number) => void,
) {
  return (
    await api.post<DocumentDetails>(`${root}/documents`, form, {
      timeout: 90_000,
      onUploadProgress: (event) =>
        progress(Math.round((event.progress ?? 0) * 100)),
    })
  ).data
}
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
export async function readProposal(id: string, signal?: AbortSignal) {
  return (
    await api.get<WikiChangesetDetails>(`${root}/wiki/changesets/${id}`, {
      signal,
    })
  ).data
}
