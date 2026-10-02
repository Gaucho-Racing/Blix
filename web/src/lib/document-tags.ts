export const maxDocumentTags = 8
export const maxDocumentTagBytes = 64

export function addDocumentTag(tags: string[], draft: string): string[] {
  if (!draft.trim()) return tags
  if (["\0", "\r", "\n"].some((character) => draft.includes(character)))
    throw new Error("Tags must use single-line text.")
  const tag = draft.trim().replace(/\s+/g, " ").toLowerCase()
  if (new TextEncoder().encode(tag).length > maxDocumentTagBytes)
    throw new Error("That tag is too long. Use a shorter label.")
  if (tags.includes(tag)) return tags
  if (tags.length >= maxDocumentTags)
    throw new Error(`Use at most ${maxDocumentTags} tags per document.`)
  return [...tags, tag]
}
