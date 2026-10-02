import type { IdentitySummary } from "@/lib/types"
import { useIdentities } from "@/lib/identity"

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
  const lookup = useIdentities(identity ? [] : [entityID])
  const resolved =
    identity ?? lookup.identities.find((item) => item.id === entityID)
  const name =
    resolved?.name ||
    (lookup.isLoading ? "Loading identity…" : "Unknown entity")
  const avatarURL = resolved?.avatar_url || resolved?.application?.icon_url
  const subtitle = resolved?.username || resolved?.application?.name

  return (
    <div className="identity-display">
      {avatarURL ? (
        <img alt="" className="identity-avatar" src={avatarURL} />
      ) : (
        <span className="identity-avatar identity-avatar-fallback">
          {initials(name) || "?"}
        </span>
      )}
      <span className="identity-copy">
        <strong>{name}</strong>
        {subtitle && <span>{subtitle}</span>}
      </span>
    </div>
  )
}
