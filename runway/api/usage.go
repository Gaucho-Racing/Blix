package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetCostSummary(c *gin.Context) {
	days, err := strconv.Atoi(c.DefaultQuery("days", "30"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	summary, err := h.usage.Summary(c.Request.Context(), days, c.Query("category"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (h *Handler) GetRunCosts(c *gin.Context) {
	breakdown, err := h.usage.RunBreakdown(c.Request.Context(), c.Param("runID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, breakdown)
}

func (h *Handler) RecordExternalUsage(c *gin.Context) {
	var request service.ExternalUsageRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	created, err := h.usage.RecordExternal(c.Request.Context(), request)
	if err != nil {
		respondError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"recorded": created})
}
