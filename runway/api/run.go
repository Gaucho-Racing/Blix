package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

type createRunRequest struct {
	AgentID string `json:"agent_id" binding:"required"`
}

type appendMessageRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *Handler) ListRuns(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	runs, err := h.executions.ListRuns(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (h *Handler) GetRun(c *gin.Context) {
	run, err := h.executions.GetRun(c.Request.Context(), c.Param("runID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

func (h *Handler) CreateRun(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	var request createRunRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	created, err := h.executions.CreateRun(c.Request.Context(), request.AgentID, caller.RunContext())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Handler) AppendMessage(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	var request appendMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	appended, err := h.executions.AppendUserMessage(c.Request.Context(), c.Param("runID"), caller.EntityID, request.Content)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, appended)
}

func (h *Handler) ListEvents(c *gin.Context) {
	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	events, err := h.executions.ListEvents(c.Request.Context(), c.Param("runID"), after)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}
