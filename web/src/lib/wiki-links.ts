export function wikiLink(
  href: string,
  path: string,
  proposedPaths: string[] = [],
) {
  if (href.startsWith("#") || href.startsWith("/knowledge/"))
    return { href, external: false }
  if (/^[a-z][a-z\d+.-]*:/i.test(href) || href.startsWith("//"))
    return { href, external: true }
  const url = new URL(
    href,
    `https://wiki.invalid/${path.split("/").map(encodeURIComponent).join("/")}`,
  )
  let target = url.pathname.slice(1)
  try {
    target = decodeURIComponent(target)
  } catch {
    target = url.pathname.slice(1)
  }
  const proposed = proposedPaths.indexOf(target)
  return {
    href:
      proposed >= 0
        ? `#file-${proposed}`
        : `/knowledge/page?path=${encodeURIComponent(target)}${url.hash}`,
    external: false,
  }
}
