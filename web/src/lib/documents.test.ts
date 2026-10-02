import axios from "axios"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import {
  controlDocument,
  documentProcessing,
  documentStatusLabel,
  importDocument,
} from "@/lib/documents"

vi.mock("@/lib/api", () => ({ api: { post: vi.fn(), get: vi.fn() } }))
vi.mock("axios", () => ({ default: { put: vi.fn() } }))

beforeEach(() => vi.clearAllMocks())

describe("document upload handoff", () => {
  it("uploads directly to Depot before queuing ingestion, without forwarding Blix credentials", async () => {
    const file = new File(["%PDF-test"], "rules.pdf", {
      type: "application/pdf",
    })
    const form = new FormData()
    form.set("file", file)
    form.set("title", "Rules")
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
    expect(await importDocument(form, vi.fn())).toBe(queued)
    expect(api.post).toHaveBeenLastCalledWith(
      "/knowledge/v1/documents/document_test/complete",
    )
  })

  it("does not queue ingestion when the binary upload fails", async () => {
    const form = new FormData()
    form.set("file", new File(["%PDF-test"], "rules.pdf"))
    vi.mocked(api.post).mockResolvedValueOnce({
      data: {
        document: { document_id: "document_test" },
        upload: { upload_url: "https://s3.example.test/signed", method: "PUT" },
      },
    })
    vi.mocked(axios.put).mockRejectedValueOnce(new Error("Upload interrupted"))
    await expect(importDocument(form, vi.fn())).rejects.toThrow(
      "Upload interrupted",
    )
    expect(api.post).toHaveBeenCalledTimes(1)
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
