import axios from "axios"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import {
  controlDocument,
  documentProcessing,
  documentIngestionProgress,
  documentEmbeddingsPending,
  documentEmbeddingStep,
  documentReadyForReview,
  documentIngestionVisible,
  type DocumentDetails,
  documentStatusLabel,
  importDocument,
  updateDocumentMetadata,
} from "@/lib/documents"

vi.mock("@/lib/api", () => ({
  api: { post: vi.fn(), get: vi.fn(), patch: vi.fn() },
}))
vi.mock("axios", () => ({ default: { put: vi.fn() } }))

beforeEach(() => vi.clearAllMocks())

describe("document upload handoff", () => {
  it("uploads directly to Depot before queuing ingestion, without forwarding Blix credentials", async () => {
    const file = new File(["%PDF-test"], "rules.pdf", {
      type: "application/pdf",
    })
    const queued = {
      document: { document_id: "document_test", status: "queued" },
    }
    vi.mocked(api.post)
      .mockResolvedValueOnce({
        data: {
          document: { document_id: "document_test" },
          upload: {
            upload_url: "https://s3.example.test/signed",
            method: "PUT",
          },
        },
      })
      .mockResolvedValueOnce({ data: queued })
    vi.mocked(axios.put).mockImplementationOnce(async (_url, _file, config) => {
      expect(api.post).toHaveBeenCalledTimes(1)
      expect(config?.headers).toEqual({ "Content-Type": "application/pdf" })
      expect(config?.headers).not.toHaveProperty("Authorization")
      return { data: null }
    })
    expect(await importDocument(file, vi.fn())).toBe(queued)
    expect(api.post).toHaveBeenNthCalledWith(
      1,
      "/knowledge/v1/documents/uploads",
      { filename: "rules.pdf" },
    )
    expect(api.post).toHaveBeenLastCalledWith(
      "/knowledge/v1/documents/document_test/complete",
    )
  })

  it("does not queue ingestion when the binary upload fails", async () => {
    const file = new File(["%PDF-test"], "rules.pdf")
    vi.mocked(api.post).mockResolvedValueOnce({
      data: {
        document: { document_id: "document_test" },
        upload: { upload_url: "https://s3.example.test/signed", method: "PUT" },
      },
    })
    vi.mocked(axios.put).mockRejectedValueOnce(new Error("Upload interrupted"))
    await expect(importDocument(file, vi.fn())).rejects.toThrow(
      "Upload interrupted",
    )
    expect(api.post).toHaveBeenCalledTimes(1)
  })

  it("rejects invalid files before creating an upload and lets users clear extracted metadata", async () => {
    await expect(
      importDocument(new File(["text"], "notes.txt"), vi.fn()),
    ).rejects.toThrow("Choose a PDF")
    await expect(
      importDocument(new File([], "rules.pdf"), vi.fn()),
    ).rejects.toThrow("Choose a PDF")
    expect(api.post).not.toHaveBeenCalled()
    vi.mocked(api.patch).mockResolvedValueOnce({
      data: { document: { metadata_revision: 3 } },
    })
    await updateDocumentMetadata("document_test", 2, {
      published_at: "",
      version: "Corrected edition",
      tags: [],
    })
    expect(api.patch).toHaveBeenCalledWith(
      "/knowledge/v1/documents/document_test/metadata",
      {
        revision: 2,
        published_at: "",
        version: "Corrected edition",
        tags: [],
      },
    )
  })

  it("polls only active processing stages and sends retries to the ingestion endpoint", async () => {
    expect(documentProcessing("analyzing_visuals")).toBe(true)
    expect(documentProcessing("indexing")).toBe(true)
    expect(documentProcessing("identifying_metadata")).toBe(true)
    expect(documentProcessing("ready")).toBe(false)
    expect(documentProcessing("failed")).toBe(false)
    expect(documentStatusLabel("partial")).toContain("coverage gaps")
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} })
    await controlDocument("document_test", "retry")
    expect(api.post).toHaveBeenCalledWith(
      "/knowledge/v1/documents/document_test/ingestion/retry",
    )
  })
})


describe("document preparation progress", () => {
  it("tracks concurrent visual analysis and metadata without waiting for reading chunks", () => {
    const details = {
      document: {
        status: "analyzing_visuals",
        stages: [
          { name: "uploading", status: "completed" },
          { name: "extracting", status: "completed" },
          { name: "indexing", status: "completed" },
          { name: "analyzing_visuals", status: "running" },
          { name: "identifying_metadata", status: "running" },
        ],
      },
      pages: [
        { image_sha256: "one", visual_analyzed_at: "saved" },
        { image_sha256: "two", visual_analyzed_at: null },
      ],
      chunks: [{ completed_at: null }],
    } as DocumentDetails
    expect(documentIngestionProgress(details)).toEqual({
      value: 3.5, max: 5, label: "1 / 2 page images analyzed",
    })
    details.document.stages[4].status = "completed"
    expect(documentIngestionProgress(details).value).toBe(4.5)
    details.document.status = "ready"
    expect(documentIngestionProgress(details)).toEqual({
      value: 5, max: 5, label: "5 / 5 steps completed",
    })
  })
})


describe("embedding generation progress", () => {
  it("keeps polling and reports retries while keyword search stays available", () => {
    const details = {
      document: { status: "ready", stages: [] }, pages: [],
      search_index: { status: "retry", total: 10, ready: 6, retrying: 4 },
    } as unknown as DocumentDetails
    expect(documentEmbeddingsPending(details)).toBe(true)
    expect(documentEmbeddingStep(details.search_index)).toEqual({ status: "running", detail: "6 / 10 chunks ready · retrying 4" })
    expect(documentIngestionProgress(details)).toEqual({ value: 5.6, max: 6, label: "5 / 6 steps completed" })
    details.search_index.status = "ready"
    details.search_index.ready = 10
    expect(documentEmbeddingsPending(details)).toBe(false)
    expect(documentIngestionProgress(details)).toEqual({ value: 6, max: 6, label: "6 / 6 steps completed" })
    expect(documentEmbeddingStep(details.search_index).status).toBe("completed")
  })

  it("does not imply disabled or excluded generation is still running", () => {
    const details = { search_index: { status: "disabled", ready: 0, total: 10 } } as DocumentDetails
    expect(documentEmbeddingsPending(details)).toBe(false)
    expect(documentEmbeddingStep(details.search_index).detail).toContain("Not configured")
    details.search_index.status = "excluded"
    expect(documentEmbeddingsPending(details)).toBe(false)
    expect(documentEmbeddingStep(details.search_index).status).toBe("cancelled")
  })
})

describe("early document review", () => {
  it("enables review after metadata and keeps unfinished approved ingestion visible", () => {
    const details = {
      document: { status: "analyzing_visuals", review_status: "pending", extracted_metadata: null, stages: [
        { name: "identifying_metadata", status: "running" },
        { name: "analyzing_visuals", status: "running" },
      ] },
      search_index: { status: "running" },
    } as DocumentDetails
    expect(documentReadyForReview(details.document)).toBe(false)
    details.document.extracted_metadata = {} as DocumentDetails["document"]["extracted_metadata"]
    details.document.stages[0].status = "completed"
    details.document.review_status = "in_review"
    expect(documentReadyForReview(details.document)).toBe(true)
    details.document.review_status = "ingested"
    expect(documentIngestionVisible(details)).toBe(true)
    details.document.status = "ready"
    expect(documentIngestionVisible(details)).toBe(true)
    details.search_index.status = "ready"
    expect(documentIngestionVisible(details)).toBe(false)
    details.document.status = "failed"
    expect(documentIngestionVisible(details)).toBe(true)
  })
})
