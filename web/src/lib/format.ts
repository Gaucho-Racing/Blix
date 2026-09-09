export function formatTimestamp(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(value))
}

export function formatRelative(value: string): string {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000)
  const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
  if (Math.abs(seconds) < 60) return relative.format(seconds, "second")
  const minutes = Math.round(seconds / 60)
  if (Math.abs(minutes) < 60) return relative.format(minutes, "minute")
  const hours = Math.round(minutes / 60)
  if (Math.abs(hours) < 24) return relative.format(hours, "hour")
  return relative.format(Math.round(hours / 24), "day")
}

export function eventLabel(type: string): string {
  return type.replace(/_v\d+$/, "").replaceAll(".", " ").replaceAll("_", " ")
}

export type IdentifierPrefix = "run" | "execution" | "event" | "call" | "message" | "model_call" | "worker" | "outbox"

const rawULIDPattern = /^[0-9a-hjkmnp-tv-z]{26}$/i

export function formatIdentifier(value: string, prefix: IdentifierPrefix): string {
  return rawULIDPattern.test(value) ? `${prefix}_${value}` : value
}

const identifierPrefixes: Record<string, IdentifierPrefix> = {
  run_id: "run",
  root_execution_id: "execution",
  execution_id: "execution",
  parent_execution_id: "execution",
  waiting_on_execution_id: "execution",
  completed_execution_id: "execution",
  event_id: "event",
  message_id: "message",
  turn_message_id: "message",
  call_id: "call",
  model_call_id: "model_call",
  worker_id: "worker",
  outbox_id: "outbox",
}

export function formatIdentifiersInJSON(value: unknown, key?: string): unknown {
  if (typeof value === "string") {
    if (key && identifierPrefixes[key]) {
      return formatIdentifier(value, identifierPrefixes[key])
    }
    const trimmed = value.trim()
    if ((trimmed.startsWith("{") && trimmed.endsWith("}")) || (trimmed.startsWith("[") && trimmed.endsWith("]"))) {
      try {
        return JSON.stringify(formatIdentifiersInJSON(JSON.parse(value)))
      } catch {
        return value
      }
    }
    return value
  }
  if (Array.isArray(value)) {
    return value.map((item) => formatIdentifiersInJSON(item))
  }
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([entryKey, entryValue]) => [entryKey, formatIdentifiersInJSON(entryValue, entryKey)]),
    )
  }
  return value
}

export function readString(payload: Record<string, unknown>, key: string): string | undefined {
  const value = payload[key]
  return typeof value === "string" ? value : undefined
}

export function readNumber(payload: Record<string, unknown>, key: string): number | undefined {
  const value = payload[key]
  return typeof value === "number" ? value : undefined
}
