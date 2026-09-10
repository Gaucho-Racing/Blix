import type { IdentitySummary } from "@/lib/types"

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase()
}

interface IdentityDisplayProps {
  entityID: string
  identity?: IdentitySummary
}

export function IdentityDisplay({ entityID, identity }: IdentityDisplayProps) {
  const name = identity?.name || "Unknown caller"
  const avatarURL = identity?.avatar_url || identity?.application?.icon_url
  const subtitle = identity?.username || identity?.application?.name || identity?.type

  return (
    <div className="identity-display">
      {avatarURL ? (
        <img alt="" className="identity-avatar" src={avatarURL} />
      ) : (
        <span className="identity-avatar identity-avatar-fallback">{initials(name) || "?"}</span>
      )}
      <span className="identity-copy">
        <strong>{name}</strong>
        {subtitle && <span>{subtitle}</span>}
        <code>{entityID || "—"}</code>
      </span>
    </div>
  )
}
