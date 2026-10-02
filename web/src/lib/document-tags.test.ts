import { describe, expect, it } from "vitest"
import { addDocumentTag } from "@/lib/document-tags"

describe("document tag editing", () => {
  it("normalizes and deduplicates tags without mutating the saved list", () => {
    const tags = ["formula sae"]
    expect(addDocumentTag(tags, " FORMULA   SAE ")).toBe(tags)
    expect(addDocumentTag(tags, " Driverless ")).toEqual([
      "formula sae",
      "driverless",
    ])
    expect(addDocumentTag(tags, "   ")).toBe(tags)
    expect(tags).toEqual(["formula sae"])
  })

  it("enforces UTF-8 size, single-line labels, and the document limit", () => {
    expect(addDocumentTag([], "é".repeat(32))).toEqual(["é".repeat(32)])
    expect(() => addDocumentTag([], "é".repeat(33))).toThrow("too long")
    expect(() => addDocumentTag([], "two\nlines")).toThrow("single-line")
    const tags = Array.from({ length: 8 }, (_, index) => String(index))
    expect(addDocumentTag(tags, "0")).toBe(tags)
    expect(() => addDocumentTag(tags, "another")).toThrow("at most 8")
  })
})
