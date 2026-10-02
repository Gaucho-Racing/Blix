package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ValidateDocumentAccess(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var request struct {
		DocumentIDs []string `json:"document_ids"`
	}
	if c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	if err := h.documents.ValidateRetrievalDocuments(c.Request.Context(), request.DocumentIDs); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"allowed": true})
}

func (h *Handler) RetrieveDocumentEvidence(c *gin.Context) {
	scope, err := h.documents.ConversationRetrievalScope(c.Request.Context(), c.Query("conversation_id"), c.Query("session_id"), c.Query("scope"))
	if err != nil {
		respondError(c, err)
		return
	}
	var result any
	switch c.Query("operation") {
	case "search":
		limit := 8
		if value := c.Query("limit"); value != "" {
			limit, err = strconv.Atoi(value)
		}
		if err == nil {
			result, err = h.documents.SearchSources(c.Request.Context(), service.DocumentSearchOptions{Query: c.Query("q"), DocumentID: c.Query("document_id"), Limit: limit}, scope)
		}
	case "list_documents":
		var documents []service.DocumentSearchContext
		documents, err = h.documents.ListRetrievableDocuments(c.Request.Context(), scope)
		result = gin.H{"documents": documents}
	case "read_page":
		var page, cursor int
		page, err = strconv.Atoi(c.Query("page"))
		if err == nil && c.Query("cursor") != "" {
			cursor, err = strconv.Atoi(c.Query("cursor"))
		}
		if err == nil {
			result, err = h.documents.ReadEvidencePage(c.Request.Context(), c.Query("document_id"), page, cursor, scope)
		}
	default:
		err = service.ErrInvalid
	}
	if err != nil {
		if _, ok := err.(*strconv.NumError); ok {
			err = service.ErrInvalid
		}
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
