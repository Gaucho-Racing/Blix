import type { DocumentCitation } from "@/lib/documents"
import { api } from "@/lib/api"

export type ChangesetStatus = "proposed" | "prepared" | "committed" | "failed" | "rejected"

export interface WikiChangeset {
  changeset_id: string
  origin: string
  conversation_id: string
  session_id: string
  season_slug: string
  type: string
  status: ChangesetStatus
  summary: string
  base_commit_sha: string
  result_commit_sha: string
  error: string
  review_decision: string
  reviewed_by: string
  review_note: string
  reviewed_at?: string
  committed_at?: string
  created_at: string
}

export interface WikiChangesetFile {
  changeset_file_id: string
  operation: "upsert" | "delete"
  path: string
  content: string
}

export interface WikiChangesetDetails {
  changeset: WikiChangeset
  files: WikiChangesetFile[]
  citations?: Record<string, DocumentCitation>
}

interface ChangesetPage {
  changesets: WikiChangeset[]
  next_cursor: string
}

export interface PageRevision {
  commit_sha: string
  exists: boolean
  content: string
}

const path = "/knowledge/v1/changesets"

export async function listChangesets(status: string, before: string): Promise<ChangesetPage> {
  const { data } = await api.get<ChangesetPage>(path, { params: { status, before, limit: 30 } })
  return data
}

export async function getChangeset(id: string): Promise<WikiChangesetDetails> {
  const { data } = await api.get<WikiChangesetDetails>(`${path}/${encodeURIComponent(id)}`)
  return data
}

export async function getChangesetBase(id: string, filePath: string): Promise<PageRevision> {
  const { data } = await api.get<PageRevision>(`${path}/${encodeURIComponent(id)}/base`, {
    params: { path: filePath }, timeout: 30_000,
  })
  return data
}

export async function reviewChangeset(id: string, decision: "approve" | "reject", note: string): Promise<WikiChangesetDetails> {
  const { data } = await api.post<WikiChangesetDetails>(`${path}/${encodeURIComponent(id)}/review/${decision}`, { note }, { timeout: 30_000 })
  return data
}
