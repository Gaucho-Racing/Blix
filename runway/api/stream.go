package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

const streamKeepaliveInterval = 15 * time.Second

type eventLoader func(context.Context, uint64) ([]model.ExecutionEvent, error)

func (h *Handler) StreamAllEvents(c *gin.Context) {
	h.streamEvents(c, "", h.executions.ListAllEvents)
}

func (h *Handler) StreamRunEvents(c *gin.Context) {
	runID := c.Param("runID")
	if err := h.authorizeRunAccess(c, runID); err != nil {
		respondError(c, err)
		return
	}
	h.streamEvents(c, runID, func(ctx context.Context, after uint64) ([]model.ExecutionEvent, error) {
		return h.executions.ListEvents(ctx, runID, after)
	})
}

func (h *Handler) streamEvents(c *gin.Context, runID string, load eventLoader) {
	cursor, err := streamCursor(c)
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	liveEvents, unsubscribe := h.events.Subscribe(runID)
	defer unsubscribe()

	batch, err := load(c.Request.Context(), cursor)
	if err != nil {
		respondError(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	for {
		for _, event := range batch {
			if event.Position <= cursor {
				continue
			}
			if err := writeStreamEvent(c, event); err != nil {
				return
			}
			cursor = event.Position
		}
		if len(batch) < 100 {
			break
		}
		batch, err = load(c.Request.Context(), cursor)
		if err != nil {
			return
		}
	}

	keepalive := time.NewTicker(streamKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, open := <-liveEvents:
			if !open {
				return
			}
			if event.Position <= cursor {
				continue
			}
			if err := writeStreamEvent(c, event); err != nil {
				return
			}
			cursor = event.Position
		case <-keepalive.C:
			if _, err := fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func streamCursor(c *gin.Context) (uint64, error) {
	queryCursor, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		return 0, err
	}
	header := c.GetHeader("Last-Event-ID")
	if header == "" {
		return queryCursor, nil
	}
	headerCursor, err := strconv.ParseUint(header, 10, 64)
	if err != nil {
		return 0, err
	}
	if headerCursor > queryCursor {
		return headerCursor, nil
	}
	return queryCursor, nil
}

func writeStreamEvent(c *gin.Context, event model.ExecutionEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "id: %d\ndata: %s\n\n", event.Position, payload); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
