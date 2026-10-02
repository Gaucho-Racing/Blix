import { expect, it } from "vitest"
import { wikiLink } from "@/lib/wiki-links"

it("resolves relative wiki paths against the source file", () => {
  expect(wikiLink("../index.md", "seasons/gr26/brakes.md").href).toBe(
    "/knowledge/page?path=seasons%2Findex.md",
  )
  expect(wikiLink("team/overview.md", "index.md").href).toBe(
    "/knowledge/page?path=team%2Foverview.md",
  )
})
it("opens proposed files within the proposal and preserves footnotes", () => {
  expect(
    wikiLink("rules/brakes.md", "index.md", ["index.md", "rules/brakes.md"])
      .href,
  ).toBe("#file-1")
  expect(wikiLink("#user-content-fn-1", "rules/brakes.md").href).toBe(
    "#user-content-fn-1",
  )
  expect(wikiLink("https://sae.org/rules", "index.md").external).toBe(true)
})

it("keeps malformed percent escapes from crashing the reader", () => {
  expect(wikiLink("bad%path.md", "index.md").href).toBe(
    "/knowledge/page?path=bad%25path.md",
  )
})
