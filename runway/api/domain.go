package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func internalAuthentication(c *gin.Context) {
	token := os.Getenv("BLIX_INTERNAL_TOKEN")
	provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(provided)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid service credentials"})
		return
	}
	c.Next()
}
func (h *Handler) authorizeConversationAccess(c *gin.Context, id string) error {
	caller, err := getCaller(c)
	if err != nil {
		return err
	}
	return h.executions.AuthorizeConversationAccess(c.Request.Context(), id, caller.EntityID, callerHasGroup(caller, adminGroup))
}
func callerHasGroup(caller service.CallerContext, group string) bool {
	for _, g := range caller.Groups {
		if g == group {
			return true
		}
	}
	return false
}
func initializeDomainRoutes(router *gin.Engine, h *Handler, auth *service.AuthService) {
	public := router.Group("/v1", RequireAuthentication(auth))
	public.POST("/conversations", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.CreateConversation(c.Request.Context(), caller.ConversationContext())
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	})
	public.GET("/conversations", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.ListConversations(c.Request.Context(), caller.EntityID, callerHasGroup(caller, adminGroup))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"conversations": result})
	})
	public.GET("/conversations/:conversationID", func(c *gin.Context) {
		id := c.Param("conversationID")
		if err := h.authorizeConversationAccess(c, id); err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.GetConversation(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	public.POST("/conversations/:conversationID/messages", func(c *gin.Context) {
		var r struct {
			Content string `json:"content"`
		}
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.AppendUserMessage(c.Request.Context(), c.Param("conversationID"), caller.EntityID, r.Content)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	public.POST("/conversations/:conversationID/interrupt", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		if err := h.executions.InterruptConversation(c.Request.Context(), c.Param("conversationID"), caller.EntityID); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	public.GET("/conversations/:conversationID/events", h.ListConversationEvents)
	router.GET("/v1/conversations/:conversationID/events/stream", RequireStreamAuthentication(auth), h.StreamConversationEvents)
	public.GET("/conversations/:conversationID/tasks", func(c *gin.Context) {
		id := c.Param("conversationID")
		if err := h.authorizeConversationAccess(c, id); err != nil {
			respondError(c, err)
			return
		}
		tasks, err := h.executions.ListTasks(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
	})
	public.GET("/tasks/:taskID", func(c *gin.Context) {
		task, err := h.executions.GetTask(c.Request.Context(), c.Param("taskID"))
		if err != nil {
			respondError(c, err)
			return
		}
		if err := h.authorizeConversationAccess(c, task.ConversationID); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, task)
	})
	public.POST("/tasks/:taskID/:operation", func(c *gin.Context) {
		var r struct {
			Content string `json:"content"`
		}
		if c.Request.ContentLength > 0 && c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		if err := h.executions.ControlTask(c.Request.Context(), c.Param("taskID"), caller.EntityID, c.Param("operation"), r.Content); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	public.POST("/approvals/:approvalID/:decision", func(c *gin.Context) {
		var r struct {
			Note string `json:"note"`
		}
		if c.Request.ContentLength > 0 && c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		if err := h.executions.DecideApproval(c.Request.Context(), c.Param("approvalID"), caller.EntityID, c.Param("decision"), r.Note); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	internal := router.Group("/internal/v1", internalAuthentication)
	internal.POST("/workers/register", h.RegisterWorker)
	internal.POST("/workers/:workerID/heartbeat", h.HeartbeatWorker)
	internal.POST("/attempts/:attemptID/claim", func(c *gin.Context) {
		var r service.ClaimRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.ClaimAttempt(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/attempts/:attemptID/heartbeat", func(c *gin.Context) {
		var r service.Ownership
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if err := h.executions.HeartbeatAttempt(c.Request.Context(), c.Param("attemptID"), r); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
	internal.POST("/attempts/:attemptID/memory", func(c *gin.Context) {
		var r struct {
			service.Ownership
			Memory json.RawMessage `json:"memory"`
		}
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		v, err := h.executions.UpdateMemory(c.Request.Context(), c.Param("attemptID"), r.Ownership, r.Memory)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"version": v})
	})

	internal.POST("/attempts/:attemptID/checkpoints", func(c *gin.Context) {
		var r service.CheckpointRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		v, err := h.executions.Checkpoint(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"version": v})
	})
	internal.POST("/attempts/:attemptID/events", func(c *gin.Context) {
		var r struct {
			service.Ownership
			Events []service.RuntimeEvent `json:"events"`
		}
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		v, err := h.executions.RecordEvents(c.Request.Context(), c.Param("attemptID"), r.Ownership, r.Events)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"version": v})
	})
	internal.POST("/attempts/:attemptID/delegations", func(c *gin.Context) {
		var r service.DelegationRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.Delegate(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	internal.POST("/attempts/:attemptID/complete", func(c *gin.Context) {
		var r service.CompletionRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if err := h.executions.Complete(c.Request.Context(), c.Param("attemptID"), r); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
	internal.POST("/attempts/:attemptID/fail", func(c *gin.Context) {
		var r service.FailureRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if err := h.executions.Fail(c.Request.Context(), c.Param("attemptID"), r); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
	internal.POST("/attempts/:attemptID/commands", func(c *gin.Context) {
		var r service.Ownership
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		commands, err := h.executions.Commands(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"commands": commands})
	})

	internal.POST("/attempts/:attemptID/task-controls", func(c *gin.Context) {
		var r service.TaskControlRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.SessionTaskControl(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/attempts/:attemptID/actions", func(c *gin.Context) {
		var r service.ActionRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.BeginAction(c.Request.Context(), c.Param("attemptID"), r)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/attempts/:attemptID/actions/:actionID/complete", func(c *gin.Context) {
		var r struct {
			service.Ownership
			Result json.RawMessage `json:"result"`
		}
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		v, err := h.executions.FinishAction(c.Request.Context(), c.Param("attemptID"), c.Param("actionID"), r.Ownership, r.Result)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"version": v})
	})
	internal.POST("/attempts/:attemptID/actions/:actionID/wait", func(c *gin.Context) {
		var r service.CheckpointRequest
		if c.ShouldBindJSON(&r) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if err := h.executions.WaitApproval(c.Request.Context(), c.Param("attemptID"), c.Param("actionID"), r); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})

}

func (h *Handler) ListConversationEvents(c *gin.Context) {
	id := c.Param("conversationID")
	if err := h.authorizeConversationAccess(c, id); err != nil {
		respondError(c, err)
		return
	}
	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	events, err := h.executions.ListEvents(c.Request.Context(), id, after)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}
