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
	c.JSON(http.StatusOK, source)
}

func (h *Handler) ListSources(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	sources, err := h.sources.FindSources(c.Request.Context(), service.SourceQuery{
		Limit: limit, Kind: c.Query("kind"), ExternalID: c.Query("external_id"), Before: c.Query("before"),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	next := ""
	if len(sources) > limit {
		sources = sources[:limit]
		next = sources[len(sources)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources, "next_before": next})
}

func (h *Handler) GetSource(c *gin.Context) {
	if err := h.documents.AuthorizeAgentSource(c.Request.Context(), c.Param("sourceID"), c.Query("conversation_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	source, err := h.sources.GetSource(c.Request.Context(), c.Param("sourceID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, source)
}

func (h *Handler) ListSourceRecords(c *gin.Context) {
	if err := h.documents.AuthorizeAgentSource(c.Request.Context(), c.Param("sourceID"), c.Query("conversation_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	records, err := h.sources.ListSourceRecords(c.Request.Context(), service.SourceRecordQuery{
		SourceID: c.Param("sourceID"), Limit: limit, ExternalID: c.Query("external_id"), Before: c.Query("before"),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	next := ""
	if len(records) > limit {
		records = records[:limit]
		next = records[len(records)-1].ID
	}
	summaries := make([]gin.H, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, gin.H{
			"source_record_id": record.ID, "source_id": record.SourceID, "external_id": record.ExternalID,
			"external_version": record.ExternalVersion, "record_type": record.RecordType,
			"external_url": record.ExternalURL, "evidence_type": record.EvidenceType, "capture_method": record.CaptureMethod,
			"occurred_at": record.OccurredAt, "ingested_at": record.IngestedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"records": summaries, "next_before": next})
}

func (h *Handler) CaptureSourceRecord(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024*1024)
	var request service.CaptureSourceRecordInput
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	ingested, err := h.sources.CaptureSourceRecord(c.Request.Context(), request)
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
	if err := h.documents.AuthorizeAgentRecord(c.Request.Context(), c.Param("sourceRecordID"), c.Query("conversation_id"), c.Query("session_id")); err != nil {
		respondError(c, err)
		return
	}
	record, err := h.sources.ReadSourceRecord(c.Request.Context(), c.Param("sourceRecordID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, record)
}
