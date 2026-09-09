package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListWorkers(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	workers, err := h.workers.List(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"workers": workers})
}

func (h *Handler) RegisterWorker(c *gin.Context) {
	var request service.WorkerRegistrationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	registration, err := h.workers.Register(c.Request.Context(), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, registration)
}

func (h *Handler) HeartbeatWorker(c *gin.Context) {
	worker, err := h.workers.Heartbeat(c.Request.Context(), c.Param("workerID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, worker)
}

func (h *Handler) StreamWorkers(c *gin.Context) {
	updates, unsubscribe := h.workerUpdates.Subscribe()
	defer unsubscribe()
	workers, err := h.workers.List(c.Request.Context(), 100)
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
	for _, worker := range workers {
		if err := writeWorkerStreamEvent(c, worker); err != nil {
			return
		}
	}

	keepalive := time.NewTicker(streamKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case worker, open := <-updates:
			if !open {
				return
			}
			if err := writeWorkerStreamEvent(c, worker); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeWorkerStreamEvent(c *gin.Context, worker service.WorkerSummary) error {
	payload, err := json.Marshal(worker)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
