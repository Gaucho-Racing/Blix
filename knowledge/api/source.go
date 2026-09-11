package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

type createSourceRequest struct {
	Kind       string          `json:"kind" binding:"required"`
	ExternalID string          `json:"external_id" binding:"required"`
	Name       string          `json:"name" binding:"required"`
	Metadata   json.RawMessage `json:"metadata"`
}

type sourceIdentityRequest struct {
	Provider    string `json:"provider" binding:"required"`
	ExternalID  string `json:"external_id" binding:"required"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
}

type ingestSourceRecordRequest struct {
	ExternalID       string                 `json:"external_id" binding:"required"`
	ExternalVersion  string                 `json:"external_version"`
	RecordType       string                 `json:"record_type" binding:"required"`
	ParentExternalID string                 `json:"parent_external_id"`
	ExternalURL      string                 `json:"external_url"`
	Content          string                 `json:"content"`
	Metadata         json.RawMessage        `json:"metadata"`
	OccurredAt       time.Time              `json:"occurred_at" binding:"required"`
	Author           *sourceIdentityRequest `json:"author"`
}

func (h *Handler) CreateSource(c *gin.Context) {
	var request createSourceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	source, err := h.sources.CreateSource(c.Request.Context(), service.CreateSourceInput{
		Kind:       request.Kind,
		ExternalID: request.ExternalID,
		Name:       request.Name,
		Metadata:   request.Metadata,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, source)
}

func (h *Handler) ListSources(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	sources, err := h.sources.ListSources(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources})
}

func (h *Handler) IngestSourceRecord(c *gin.Context) {
	var request ingestSourceRecordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	var author *service.SourceIdentityInput
	if request.Author != nil {
		author = &service.SourceIdentityInput{
			Provider:    request.Author.Provider,
			ExternalID:  request.Author.ExternalID,
			DisplayName: request.Author.DisplayName,
			Username:    request.Author.Username,
		}
	}
	ingested, err := h.sources.IngestSourceRecord(c.Request.Context(), c.Param("sourceID"), service.IngestSourceRecordInput{
		ExternalID:       request.ExternalID,
		ExternalVersion:  request.ExternalVersion,
		RecordType:       request.RecordType,
		ParentExternalID: request.ParentExternalID,
		ExternalURL:      request.ExternalURL,
		Content:          request.Content,
		Metadata:         request.Metadata,
		OccurredAt:       request.OccurredAt,
		Author:           author,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	status := http.StatusOK
	if ingested.Created {
		status = http.StatusCreated
	}
	c.JSON(status, ingested)
}

func (h *Handler) GetSourceRecord(c *gin.Context) {
	record, err := h.sources.GetSourceRecord(c.Request.Context(), c.Param("sourceRecordID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, record)
}
