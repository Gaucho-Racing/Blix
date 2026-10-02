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
func (h *Handler) authorizeRunAccess(c *gin.Context, id string) error {
	caller, err := getCaller(c)
	if err != nil {
		return err
	}
	return h.executions.AuthorizeRunAccess(c.Request.Context(), id, caller.EntityID, callerHasGroup(caller, adminGroup))
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
	public.GET("/admin/runs", RequireGroup(adminGroup), h.ListAdminRuns)
	public.GET("/document-chats/:documentID", RequireGroup("BlixUsers", "BlixAdmins"), func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil || caller.EntityType != "USER" {
			respondError(c, service.ErrForbidden)
			return
		}
		run, err := h.executions.GetDocumentChat(c.Request.Context(), c.Param("documentID"), caller.EntityID)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"run": run})
	})
	public.POST("/document-chats/:documentID", RequireGroup("BlixUsers", "BlixAdmins"), func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.CreateDocumentChat(c.Request.Context(), c.Param("documentID"), caller.RunContext())
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	})
	public.POST("/runs", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		var request struct {
			DocumentIDs []string `json:"document_ids"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
		if c.Request.ContentLength != 0 && c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if len(request.DocumentIDs) > 0 && !callerHasGroup(caller, "BlixUsers") && !callerHasGroup(caller, adminGroup) {
			respondError(c, service.ErrForbidden)
			return
		}
		result, err := h.executions.CreateRunWithDocuments(c.Request.Context(), caller.RunContext(), request.DocumentIDs)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	})
	public.GET("/runs", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.ListRuns(c.Request.Context(), caller.EntityID, callerHasGroup(caller, adminGroup), c.Query("kind"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": result})
	})
	public.GET("/runs/:runID", func(c *gin.Context) {
		id := c.Param("runID")
		if err := h.authorizeRunAccess(c, id); err != nil {
			respondError(c, err)
			return
		}
		result, err := h.executions.GetRun(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	public.POST("/runs/:runID/messages", func(c *gin.Context) {
		var r struct {
			Content        string `json:"content"`
			DisplayContent string `json:"display_content"`
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
		result, err := h.executions.AppendUserMessageWithDisplay(c.Request.Context(), c.Param("runID"), caller.EntityID, r.Content, r.DisplayContent)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	public.POST("/runs/:runID/interrupt", func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			return
		}
		if err := h.executions.InterruptRun(c.Request.Context(), c.Param("runID"), caller.EntityID); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	public.GET("/runs/:runID/events", h.ListRunEvents)
	router.GET("/v1/runs/:runID/events/stream", RequireStreamAuthentication(auth), h.StreamRunEvents)
	public.GET("/runs/:runID/tasks", func(c *gin.Context) {
		id := c.Param("runID")
		if err := h.authorizeRunAccess(c, id); err != nil {
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
		if err := h.authorizeRunAccess(c, task.RunID); err != nil {
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
	internal.GET("/attempts/ready", func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if err != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		attempts, err := h.executions.ReadyAttempts(c.Request.Context(), c.Query("runtime_kind"), limit)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"attempts": attempts})
	})
	internal.GET("/runs/:runID/retrieval-context", func(c *gin.Context) {
		result, err := h.executions.RetrievalContext(c.Request.Context(), c.Param("runID"), c.Query("session_id"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/document-attempts/validate", func(c *gin.Context) {
		var request service.DocumentAttemptAccess
		if c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.ValidateDocumentAttempt(c.Request.Context(), request)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/document-ingestions", func(c *gin.Context) {
		var request service.DocumentIngestionRequest
		if c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		result, err := h.executions.StartDocumentIngestion(c.Request.Context(), request)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	internal.GET("/tasks/:taskID", func(c *gin.Context) {
		result, err := h.executions.GetTask(c.Request.Context(), c.Param("taskID"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	internal.POST("/tasks/:taskID/:operation", func(c *gin.Context) {
		var request struct {
			Owner string `json:"owner"`
		}
		if c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		if err := h.executions.ControlTask(c.Request.Context(), c.Param("taskID"), request.Owner, c.Param("operation"), ""); err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
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

func (h *Handler) ListAdminRuns(c *gin.Context) {
	result, err := h.executions.ListAllRuns(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": result})
}

func (h *Handler) ListRunEvents(c *gin.Context) {
	id := c.Param("runID")
	if err := h.authorizeRunAccess(c, id); err != nil {
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
