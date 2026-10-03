package service

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

type DocumentSearchIndex struct {
	Status         string `json:"status"`
	Model          string `json:"model"`
	Dimensions     int    `json:"dimensions"`
	Namespace      string `json:"namespace"`
	Total          int    `json:"total"`
	Ready          int    `json:"ready"`
	Pending        int    `json:"pending"`
	Processing     int    `json:"processing"`
	Retrying       int    `json:"retrying"`
	TextChunks     int    `json:"text_chunks"`
	VisualChunks   int    `json:"visual_chunks"`
	SourceRecords  int    `json:"source_records"`
	IndexedRecords int    `json:"indexed_records"`
}

type DocumentIndexChunk struct {
	model.SourceChunk
	PDFPage              int        `json:"pdf_page"`
	Kind                 string     `json:"kind"`
	IndexedAt            *time.Time `json:"indexed_at"`
	EmbeddingStatus      string     `json:"embedding_status"`
	EmbeddingAttempts    int        `json:"embedding_attempts"`
	EmbeddingUpdatedAt   *time.Time `json:"embedding_updated_at"`
	EmbeddingAvailableAt *time.Time `json:"embedding_available_at"`
	EmbeddingError       string     `json:"embedding_error"`
}

type DocumentIndexDetails struct {
	SearchIndex DocumentSearchIndex  `json:"search_index"`
	Chunks      []DocumentIndexChunk `json:"chunks"`
	Offset      int                  `json:"offset"`
	NextOffset  *int                 `json:"next_offset"`
}

func (s *DocumentService) documentIndexQuery(tx *gorm.DB, id string) *gorm.DB {
	namespace := ""
	if s.embeddings != nil {
		namespace = s.embeddings.namespace
	}
	return tx.Table("knowledge_source_chunk c").
		Joins("JOIN knowledge_document_page p ON (p.source_record_id=c.source_record_id OR p.visual_source_record_id=c.source_record_id)").
		Joins("JOIN knowledge_source_record r ON r.id=c.source_record_id").
		Joins("JOIN knowledge_source_record_index i ON i.source_record_id=r.id AND i.chunker_version=c.chunker_version AND i.content_hash=r.content_hash").
		Joins("LEFT JOIN knowledge_embedding e ON e.namespace=? AND e.content_hash=c.content_hash", namespace).
		Where("p.document_id=? AND c.chunker_version=?", id, sourceChunkerVersion)
}

func (s *DocumentService) documentSearchIndex(tx *gorm.DB, document model.Document) (DocumentSearchIndex, error) {
	summary := DocumentSearchIndex{}
	err := s.documentIndexQuery(tx, document.ID).Select(`count(*) AS total,
 count(*) FILTER (WHERE e.status='ready' AND e.vector IS NOT NULL) AS ready,
 count(*) FILTER (WHERE e.status IS NULL OR e.status='pending') AS pending,
 count(*) FILTER (WHERE e.status='processing') AS processing,
 count(*) FILTER (WHERE e.status='retry') AS retrying,
 count(*) FILTER (WHERE c.source_record_id=p.source_record_id) AS text_chunks,
 count(*) FILTER (WHERE c.source_record_id=p.visual_source_record_id) AS visual_chunks`).Scan(&summary).Error
	if err != nil {
		return summary, err
	}
	var records struct {
		SourceRecords  int
		IndexedRecords int
	}
	err = tx.Raw(`SELECT count(*) AS source_records, count(i.source_record_id) AS indexed_records
 FROM knowledge_document_page p JOIN knowledge_source_record r ON (r.id=p.source_record_id OR r.id=p.visual_source_record_id)
 LEFT JOIN knowledge_source_record_index i ON i.source_record_id=r.id AND i.chunker_version=? AND i.content_hash=r.content_hash
 WHERE p.document_id=?`, sourceChunkerVersion, document.ID).Scan(&records).Error
	if err != nil {
		return summary, err
	}
	summary.SourceRecords, summary.IndexedRecords = records.SourceRecords, records.IndexedRecords
	if s.embeddings == nil {
		summary.Status = "disabled"
		return summary, nil
	}
	summary.Model = s.embeddings.model
	summary.Dimensions = s.embeddings.dimensions
	summary.Namespace = s.embeddings.namespace
	switch {
	case document.ReviewStatus == model.DocumentReviewRejected:
		summary.Status = "excluded"
	case summary.Retrying > 0:
		summary.Status = "retry"
	case summary.Ready == summary.Total && summary.IndexedRecords == summary.SourceRecords && summary.SourceRecords > 0 && (document.Status == "ready" || document.Status == "partial" || document.Status == "no_changes" || document.Status == "imported" || document.Status == "cancelled" || document.Status == "failed"):
		summary.Status = "ready"
	case summary.Total > 0:
		summary.Status = "running"
	default:
		summary.Status = "waiting"
	}
	return summary, nil
}

func (s *DocumentService) IndexDetails(ctx context.Context, id string, offset, limit int) (DocumentIndexDetails, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return DocumentIndexDetails{}, ErrInvalid
	}
	if !identifier.IsValid(id, identifier.Document) {
		return DocumentIndexDetails{}, ErrInvalid
	}
	result := DocumentIndexDetails{Chunks: []DocumentIndexChunk{}, Offset: offset}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document model.Document
		if err := tx.First(&document, "id=?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		var err error
		result.SearchIndex, err = s.documentSearchIndex(tx, document)
		if err != nil {
			return err
		}
		return s.documentIndexQuery(tx, id).Select(`c.*, p.number AS pdf_page,
 CASE WHEN c.source_record_id=p.source_record_id THEN 'text' ELSE 'visual' END AS kind,
 i.indexed_at, COALESCE(e.status, 'pending') AS embedding_status,
 COALESCE(e.attempts, 0) AS embedding_attempts, e.updated_at AS embedding_updated_at,
 e.available_at AS embedding_available_at, COALESCE(e.last_error, '') AS embedding_error`).
			Order("p.number, kind, c.ordinal, c.id").Offset(offset).Limit(limit + 1).Scan(&result.Chunks).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return DocumentIndexDetails{}, err
	}

	if len(result.Chunks) > limit {
		next := offset + limit
		result.NextOffset = &next
		result.Chunks = result.Chunks[:limit]
	}
	if s.embeddings == nil {
		for i := range result.Chunks {
			result.Chunks[i].EmbeddingStatus = "disabled"
		}
	}
	return result, nil
}
