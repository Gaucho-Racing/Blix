package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListKnowledgeChangesets(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.knowledge.ListChangesets(c.Request.Context(), limit, c.Query("status"), c.Query("before"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}

func (h *Handler) GetKnowledgeChangeset(c *gin.Context) {
	result, err := h.knowledge.GetChangeset(c.Request.Context(), c.Param("changesetID"), nil)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}

func (h *Handler) GetKnowledgeChangesetBase(c *gin.Context) {
	path := c.Query("path")
	result, err := h.knowledge.GetChangeset(c.Request.Context(), c.Param("changesetID"), &path)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}

func (h *Handler) ReviewKnowledgeChangeset(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var request struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.knowledge.ReviewChangeset(c.Request.Context(), c.Param("changesetID"), c.Param("decision"), caller.EntityID, request.Note)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}
