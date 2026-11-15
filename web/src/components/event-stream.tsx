import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import { api } from "@/lib/api"
import { loadSession } from "@/lib/session"
import type { WorkerSummary } from "@/lib/types"

type StreamStatus = "connecting" | "live" | "reconnecting"

interface StreamIndicatorProps {
  status: StreamStatus
}

function StreamIndicator({ status }: StreamIndicatorProps) {
  return <span className={`live-indicator stream-${status}`}><i /> {status.toUpperCase()}</span>
}

function openEventStream<T>(
  path: string,
  onEvent: (event: T) => void,
  setStatus: (status: StreamStatus) => void,
): () => void {
  let closed = false
  let source: EventSource | undefined
  let reconnectTimer: number | undefined

  const connect = () => {
    if (closed) return
    const session = loadSession()
    if (!session) return
    const url = new URL(path, window.location.origin)
    url.searchParams.set("access_token", session.accessToken)
    source = new EventSource(url)
    source.onopen = () => setStatus("live")
    source.onmessage = (message) => {
      try {
        onEvent(JSON.parse(message.data) as T)
      } catch {
        return
      }
    }
    source.onerror = () => {
      source?.close()
      if (closed) return
      setStatus("reconnecting")
      void api.get("/v1/auth/session").finally(() => {
        if (closed) return
        reconnectTimer = window.setTimeout(connect, 1_000)
      })
    }
  }
  connect()
  return () => {
    closed = true
    source?.close()
    if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
  }
}

export function WorkerListEventStream() {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<StreamStatus>("connecting")

  useEffect(() => {
    const close = openEventStream<WorkerSummary>(
      "/api/v1/workers/stream",
      (worker) => {
        queryClient.setQueryData<WorkerSummary[]>(["workers"], (current = []) => {
          const remaining = current.filter((candidate) => candidate.worker_id !== worker.worker_id)
          return [worker, ...remaining].sort(
            (left, right) => new Date(right.last_seen_at).getTime() - new Date(left.last_seen_at).getTime(),
          )
        })
      },
      setStatus,
    )
    return close
  }, [queryClient])

  return <StreamIndicator status={status} />
}
