import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import type { ExecutionEvent } from "@/lib/types"

type StreamStatus = "connecting" | "live" | "reconnecting"

interface StreamIndicatorProps {
  status: StreamStatus
}

function StreamIndicator({ status }: StreamIndicatorProps) {
  return <span className={`live-indicator stream-${status}`}><i /> {status.toUpperCase()}</span>
}

function openEventStream(
  path: string,
  onEvent: (event: ExecutionEvent) => void,
  setStatus: (status: StreamStatus) => void,
): () => void {
  const source = new EventSource(path)
  source.onopen = () => setStatus("live")
  source.onerror = () => setStatus("reconnecting")
  source.onmessage = (message) => {
    try {
      onEvent(JSON.parse(message.data) as ExecutionEvent)
    } catch {
      return
    }
  }
  return () => source.close()
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
    const close = openEventStream(`/api/v1/events/stream?after=${cursor}`, () => {
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
    const close = openEventStream(
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
