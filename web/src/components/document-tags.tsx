export function DocumentTags({ tags }: { tags: string[] }) {
  if (!tags.length) return null
  return (
    <div className="document-tags" aria-label="Document tags">
      {tags.map((tag) => (
        <span className="document-tag" key={tag}>
          {tag}
        </span>
      ))}
    </div>
  )
}
