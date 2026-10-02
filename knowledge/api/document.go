package api

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ImportDocument(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxDocumentBytes+64*1024)
	if err := c.Request.ParseMultipartForm(1024 * 1024); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	date, err := time.Parse("2006-01-02", c.PostForm("published_at"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	defer file.Close()
	result, err := h.documents.Import(c.Request.Context(), file, service.DocumentMetadata{Title: c.PostForm("title"), Publisher: c.PostForm("publisher"), Version: c.PostForm("version"), Kind: c.PostForm("kind"), ExternalURL: c.PostForm("external_url"), PublishedAt: date, UploadedBy: caller.EntityID, Filename: header.Filename})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}
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
	path, name, err := h.documents.File(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		respondError(c, err)
		return
	}
	if _, err := os.Stat(path); err != nil {
		respondError(c, service.ErrUnavailable)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", "application/pdf")
	c.FileAttachment(path, name)
}
