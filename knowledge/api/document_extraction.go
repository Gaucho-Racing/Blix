package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ExtractDocument(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 14*1024*1024)
	id := c.Param("documentID")
	var result any = gin.H{"ok": true}
	var err error
	switch c.Param("operation") {
	case "prepare", "file", "complete":
		var request service.DocumentExecution
		if c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		switch c.Param("operation") {
		case "prepare":
			result, err = h.documents.ExtractionManifest(c.Request.Context(), id, request)
		case "complete":
			err = h.documents.CompleteExtraction(c.Request.Context(), id, request)
		case "file":
			var data []byte
			data, err = h.documents.ExtractionFile(c.Request.Context(), id, request)
			if err == nil {
				c.Data(http.StatusOK, "application/pdf", data)
				return
			}
		}
	case "start":
		var request service.ExtractionStart
		if c.ShouldBindJSON(&request) != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		err = h.documents.StartExtraction(c.Request.Context(), id, request)
	default:
		err = service.ErrInvalid
	}
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) SaveExtractionPage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 14*1024*1024)
	var request service.ExtractionPage
	number, err := strconv.Atoi(c.Param("number"))
	if err != nil || c.ShouldBindJSON(&request) != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	if err := h.documents.SaveExtractionPage(c.Request.Context(), c.Param("documentID"), number, request); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
