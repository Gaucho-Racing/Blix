package api

import (
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ValidateDocumentAccess(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var request struct {
		DocumentIDs []string `json:"document_ids"`
		Mode        string   `json:"mode"`
	}
	if c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	var err error
	switch request.Mode {
	case "", "team":
		err = h.documents.ValidateRetrievalDocuments(c.Request.Context(), request.DocumentIDs)
	case "questions":
		if len(request.DocumentIDs) != 1 {
			err = service.ErrInvalid
		} else {
			err = h.documents.ValidateQuestionDocument(c.Request.Context(), request.DocumentIDs[0])
		}
	default:
		err = service.ErrInvalid
	}
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"allowed": true})
}

func (h *Handler) GetAgentDocument(c *gin.Context) {
	if err := h.documents.AuthorizeAgentDocument(c.Request.Context(), c.Param("documentID"), c.Query("run_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	h.GetDocument(c)
}

func (h *Handler) ReadAgentDocumentChunk(c *gin.Context) {
	if err := h.documents.AuthorizeAgentDocument(c.Request.Context(), c.Param("documentID"), c.Query("run_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	h.ReadDocumentChunk(c)
}

func (h *Handler) RetrieveDocumentEvidence(c *gin.Context) {
	scope, err := h.documents.RunRetrievalScope(c.Request.Context(), c.Query("run_id"), c.Query("session_id"), c.Query("scope"))
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
	case "read_page_image":
		var page int
		page, err = strconv.Atoi(c.Query("page"))
		if err == nil {
			result, err = h.documents.EvidenceImage(c.Request.Context(), c.Query("document_id"), page, scope)
		}
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

func (h *Handler) GetAgentPageImage(c *gin.Context) {
	scope, err := h.documents.AgentImageScope(c.Request.Context(), c.Query("run_id"), c.Query("session_id"))
	if err == nil {
		err = h.documents.AuthorizeImage(c.Request.Context(), c.Param("documentID"), scope)
	}
	if err != nil {
		respondError(c, err)
		return
	}
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	data, asset, err := h.documents.ReadPageImage(c.Request.Context(), c.Param("documentID"), number)
	if err != nil {
		respondError(c, err)
		return
	}
	if err := h.documents.AuthorizeImage(c.Request.Context(), c.Param("documentID"), scope); err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"document_id": asset.DocumentID, "page": asset.Number, "sha256": asset.SHA256, "data_url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)})
}

func (h *Handler) GetDocumentPageImage(c *gin.Context) {
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	data, _, err := h.documents.ReadPageImage(c.Request.Context(), c.Param("documentID"), number)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "image/jpeg", data)
}

func (h *Handler) ReadAgentDocumentPage(c *gin.Context) {
	if err := h.documents.AuthorizeAgentDocument(c.Request.Context(), c.Param("documentID"), c.Query("run_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	h.GetDocumentPage(c)
}
