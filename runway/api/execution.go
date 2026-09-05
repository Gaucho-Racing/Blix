package api

import (
	"net/http"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

type claimExecutionRequest struct {
	WorkerID string `json:"worker_id" binding:"required"`
}

func (h *Handler) ClaimExecution(c *gin.Context) {
	var request claimExecutionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	claimed, err := h.executions.ClaimExecution(c.Request.Context(), c.Param("executionID"), request.WorkerID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, claimed)
}

func (h *Handler) DelegateExecution(c *gin.Context) {
	var request service.Delegation
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	delegated, err := h.executions.DelegateExecution(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, delegated)
}

func (h *Handler) GetExecutionResult(c *gin.Context) {
	result, err := h.executions.GetExecutionResult(
		c.Request.Context(),
		c.Param("executionID"),
		c.Query("parent_execution_id"),
		c.Query("worker_id"),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) CheckpointTool(c *gin.Context) {
	var request service.ToolCheckpoint
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	version, err := h.executions.CheckpointTool(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func (h *Handler) CheckpointModel(c *gin.Context) {
	var request service.ModelCheckpoint
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	version, err := h.executions.CheckpointModel(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func (h *Handler) CompleteExecution(c *gin.Context) {
	var request service.Completion
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	appended, err := h.executions.CompleteExecution(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, appended)
}

func (h *Handler) CompleteAgentExecution(c *gin.Context) {
	var request service.AgentCompletion
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	event, err := h.executions.CompleteAgentExecution(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, event)
}

func (h *Handler) FailExecution(c *gin.Context) {
	var request service.Failure
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	event, err := h.executions.FailExecution(c.Request.Context(), c.Param("executionID"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, event)
}
