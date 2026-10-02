import axios from "axios"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import {
  controlDocument,
  documentProcessing,
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
    })
    expect(api.patch).toHaveBeenCalledWith(
      "/knowledge/v1/documents/document_test/metadata",
      {
        revision: 2,
        published_at: "",
        version: "Corrected edition",
      },
    )
  })

  it("polls only active processing stages and sends retries to the ingestion endpoint", async () => {
    expect(documentProcessing("reading")).toBe(true)
    expect(documentProcessing("ready")).toBe(false)
    expect(documentProcessing("failed")).toBe(false)
    expect(documentStatusLabel("partial")).toContain("coverage warnings")
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} })
    await controlDocument("document_test", "retry")
    expect(api.post).toHaveBeenCalledWith(
      "/knowledge/v1/documents/document_test/ingestion/retry",
    )
  })
})
