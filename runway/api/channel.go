package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SubmitChannelInput(c *gin.Context) {
	if c.GetHeader("X-Blix-Caller-Token") == "" {
		respondError(c, service.ErrUnauthorized)
		return
	}
	caller, err := h.auth.Authenticate(c.Request.Context(), c.GetHeader("X-Blix-Caller-Token"))
	if err != nil {
		respondError(c, err)
		return
	}
	if caller.EntityType != "USER" || (!callerHasGroup(caller, "BlixUsers") && !callerHasGroup(caller, adminGroup)) {
		respondError(c, service.ErrForbidden)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32000)
	var input service.ChannelInput
	if c.ShouldBindJSON(&input) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.executions.SubmitChannelInput(c.Request.Context(), caller.RunContext(), input)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}

func (h *Handler) ChannelRunEvents(c *gin.Context) {
	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.executions.ListEvents(c.Request.Context(), c.Param("runID"), after)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": result})
}
