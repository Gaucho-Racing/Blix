import type { ExecutionEvent } from "@/lib/types"

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

export function formatDuration(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ${minutes % 60}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

export function formatCompactNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value)
}

export function eventLabel(type: string): string {
  return type.replace(/_v\d+$/, "").replaceAll(".", " ").replaceAll("_", " ")
}

export function modelEventSummary(event: ExecutionEvent): string {
  const model = readString(event.payload, "model") ?? "model"
  const sequence = readNumber(event.payload, "sequence") ?? 1
  const attempt = readNumber(event.payload, "attempt") || 1
  const prefix = `${model} · call ${sequence} · attempt ${attempt}`
  if (event.type === "model.call_v1") return `${prefix} · request sent`
  const elapsed = readNumber(event.payload, "elapsed_ms")
  const duration = elapsed === undefined ? "" : ` · ${(elapsed / 1000).toFixed(1)}s`
  if (event.type === "model.failure_v1") {
    const error = event.payload.error
    const message = error && typeof error === "object" && !Array.isArray(error)
      ? readString(error as Record<string, unknown>, "message") ?? "Request failed"
      : "Request failed"
    const status = readNumber(event.payload, "http_status")
    const retry = readNumber(event.payload, "retry_in_seconds")
    return `${prefix}${duration} · ${status ? `HTTP ${status} · ` : ""}${message} · ${retry === undefined ? "no further retry" : `retry in ${retry.toFixed(1)}s`}`
  }
  const input = readNumber(event.payload, "input_tokens") ?? 0
  const output = readNumber(event.payload, "output_tokens") ?? 0
  return `${prefix}${duration} · ${input.toLocaleString()} in / ${output.toLocaleString()} out`
}

export function formatIdentifiersInJSON(value: unknown): unknown {
  if (typeof value === "string") {
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
      Object.entries(value).map(([entryKey, entryValue]) => [entryKey, formatIdentifiersInJSON(entryValue)]),
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
