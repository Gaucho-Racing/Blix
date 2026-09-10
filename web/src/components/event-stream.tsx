import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import { api } from "@/lib/api"
import { loadSession } from "@/lib/session"
import type { ExecutionEvent, WorkerSummary } from "@/lib/types"

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

interface RunListEventStreamProps {
  initialCursor: number
}

export function RunListEventStream({ initialCursor }: RunListEventStreamProps) {
  const queryClient = useQueryClient()
  const [cursor] = useState(initialCursor)
  const [status, setStatus] = useState<StreamStatus>("connecting")

  useEffect(() => {
    let refreshTimer: number | undefined
    const close = openEventStream<ExecutionEvent>(`/api/v1/events/stream?after=${cursor}`, () => {
      if (refreshTimer !== undefined) return
      refreshTimer = window.setTimeout(() => {
        refreshTimer = undefined
        void queryClient.invalidateQueries({ queryKey: ["runs"] })
      }, 1_000)
    }, setStatus)
    return () => {
      close()
      if (refreshTimer !== undefined) window.clearTimeout(refreshTimer)
    }
  }, [cursor, queryClient])

  return <StreamIndicator status={status} />
}

interface RunEventStreamProps {
  runID: string
  initialCursor: number
}

export function RunEventStream({ runID, initialCursor }: RunEventStreamProps) {
  const queryClient = useQueryClient()
  const [stream] = useState({ runID, cursor: initialCursor })
  const [status, setStatus] = useState<StreamStatus>("connecting")

  useEffect(() => {
    let refreshTimer: number | undefined
    const pendingExecutionIDs = new Set<string>()
    const close = openEventStream<ExecutionEvent>(
      `/api/v1/runs/${stream.runID}/events/stream?after=${stream.cursor}`,
      (event) => {
        pendingExecutionIDs.add(event.execution_id)
        queryClient.setQueryData<ExecutionEvent[]>(["run-events", stream.runID], (current = []) => {
          if (current.some((existing) => existing.position === event.position)) return current
          return [...current, event]
        })
        if (refreshTimer !== undefined) return
        refreshTimer = window.setTimeout(() => {
          refreshTimer = undefined
          void queryClient.invalidateQueries({ queryKey: ["run", stream.runID] })
          for (const executionID of pendingExecutionIDs) {
            void queryClient.invalidateQueries({ queryKey: ["execution", executionID] })
          }
          pendingExecutionIDs.clear()
        }, 500)
      },
      setStatus,
    )
    return () => {
      close()
      if (refreshTimer !== undefined) window.clearTimeout(refreshTimer)
    }
  }, [queryClient, stream])

  return <StreamIndicator status={status} />
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
