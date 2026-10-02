package api

import (
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListDocuments(c *gin.Context) {
	result, err := h.documents.List(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"documents": result})
}
func (h *Handler) GetDocument(c *gin.Context) {
	result, err := h.documents.Get(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) GetDocumentPage(c *gin.Context) {
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.documents.Page(c.Request.Context(), c.Param("documentID"), number)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) GetDocumentFile(c *gin.Context) {
	data, name, err := h.documents.ReadOriginal(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Data(http.StatusOK, "application/pdf", data)
}
func (h *Handler) InitiateDocumentUpload(c *gin.Context) {
	var request struct {
		Title       string `json:"title"`
		Filename    string `json:"filename"`
		Publisher   string `json:"publisher"`
		Version     string `json:"version"`
		Kind        string `json:"kind"`
		ExternalURL string `json:"external_url"`
		PublishedAt string `json:"published_at"`
		Objective   string `json:"objective"`
	}
	if c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	date, err := time.Parse("2006-01-02", request.PublishedAt)
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.documents.InitiateUpload(c.Request.Context(), service.DocumentMetadata{Title: request.Title, Filename: request.Filename, Publisher: request.Publisher, Version: request.Version, Kind: request.Kind, ExternalURL: request.ExternalURL, PublishedAt: date, UploadedBy: caller.EntityID}, caller, request.Objective)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}
func (h *Handler) CompleteDocumentUpload(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	result, err := h.documents.CompleteUpload(c.Request.Context(), c.Param("documentID"), caller.EntityID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}
func (h *Handler) ControlDocument(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	result, err := h.documents.Control(c.Request.Context(), c.Param("documentID"), caller.EntityID, c.Param("operation"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}
func (h *Handler) ReadDocumentChunk(c *gin.Context) {
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.documents.ReadChunk(c.Request.Context(), c.Param("documentID"), number)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) ProcessDocument(c *gin.Context) {
	var request struct {
		service.DocumentExecution
		Outcome string `json:"outcome"`
	}
	if c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	var result service.DocumentDetails
	var err error
	switch c.Param("operation") {
	case "prepare":
		result, err = h.documents.Prepare(c.Request.Context(), c.Param("documentID"), request.DocumentExecution)
	case "reconcile":
		result, err = h.documents.Reconcile(c.Request.Context(), c.Param("documentID"), request.DocumentExecution)
	case "finish":
		result, err = h.documents.Finish(c.Request.Context(), c.Param("documentID"), request.DocumentExecution, request.Outcome)
	default:
		err = service.ErrInvalid
	}
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) SaveDocumentChunk(c *gin.Context) {
	var request service.SaveDocumentChunk
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil || c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.documents.SaveChunk(c.Request.Context(), c.Param("documentID"), number, request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
