import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import {
  getChangeset,
  getChangesetBase,
  reviewChangeset,
} from "@/lib/knowledge"

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), post: vi.fn() } }))
beforeEach(() => vi.clearAllMocks())

describe("shared changeset review API", () => {
  it("reads the changeset and its revision-pinned base through shared routes", async () => {
    vi.mocked(api.get).mockResolvedValue({ data: {} })
    const signal = new AbortController().signal
    await getChangeset("changeset_test", signal)
    expect(api.get).toHaveBeenCalledWith(
      "/knowledge/v1/changesets/changeset_test",
      { signal },
    )
    await getChangesetBase("changeset_test", "team/brakes.md")
    expect(api.get).toHaveBeenCalledWith(
      "/knowledge/v1/changesets/changeset_test/base",
      { params: { path: "team/brakes.md" }, timeout: 30_000 },
    )
  })

  it("submits a decision and reason, leaving reviewer identity to the server", async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} })
    await reviewChangeset(
      "changeset_test",
      "reject",
      "The exception is missing.",
    )
    expect(api.post).toHaveBeenCalledWith(
      "/knowledge/v1/changesets/changeset_test/review/reject",
      { note: "The exception is missing." },
      { timeout: 30_000 },
    )
  })
})
