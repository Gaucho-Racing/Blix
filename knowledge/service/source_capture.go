package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

type CaptureSourceRecordInput struct {
	SourceID string             `json:"source_id"`
	Source   *CreateSourceInput `json:"source"`
	IngestSourceRecordInput
}

func (s *SourceService) CaptureSourceRecord(ctx context.Context, input CaptureSourceRecordInput) (IngestedSourceRecord, error) {
	if (input.SourceID == "") == (input.Source == nil) ||
		!identifier.IsValid(input.CaptureRunID, identifier.Run) ||
		!identifier.IsValid(input.CaptureSessionID, identifier.Session) ||
		input.CaptureMethod != "agent_relay" ||
		(input.EvidenceType != "quoted_source" && input.EvidenceType != "agent_summary") ||
		strings.TrimSpace(input.Content) == "" {
		return IngestedSourceRecord{}, fmt.Errorf("%w: provide one source reference, capture provenance, evidence type, and content", ErrInvalid)
	}
	var result IngestedSourceRecord
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := NewSourceService(tx)
		if input.Source != nil {
			source, err := service.CreateSource(ctx, *input.Source)
			if err != nil {
				return err
			}
			input.SourceID = source.ID
		}
		var err error
		result, err = service.IngestSourceRecord(ctx, input.SourceID, input.IngestSourceRecordInput)
		return err
	})
	return result, err
}

type SourceQuery struct {
	Kind       string
	ExternalID string
	Before     string
	Limit      int
}

type SourceRecordDetail struct {
	VisualRecord *model.SourceRecord     `json:"visual_record,omitempty"`
	PageImage    *DocumentImageReference `json:"page_image,omitempty"`
	model.SourceRecord
	Author *model.SourceIdentity `json:"author"`
}

func (s *SourceService) ReadSourceRecord(ctx context.Context, recordID string) (SourceRecordDetail, error) {
	record, err := s.GetSourceRecord(ctx, recordID)
	if err != nil {
		return SourceRecordDetail{}, err
	}
	detail := SourceRecordDetail{SourceRecord: record}
	if record.AuthorSourceIdentityID != nil {
		var author model.SourceIdentity
		if err := s.database.WithContext(ctx).First(&author, "id = ?", *record.AuthorSourceIdentityID).Error; err != nil {
			return SourceRecordDetail{}, mapDatabaseError(err)
		}
		detail.Author = &author
	}
	return detail, nil
}

type SourceRecordQuery struct {
	SourceID   string
	ExternalID string
	Before     string
	Limit      int
}

func (s *SourceService) FindSources(ctx context.Context, query SourceQuery) ([]model.Source, error) {
	if query.Limit < 1 || query.Limit > 100 || (query.Before != "" && !identifier.IsValid(query.Before, identifier.Source)) {
		return nil, fmt.Errorf("%w: invalid limit or source cursor", ErrInvalid)
	}
	db := s.database.WithContext(ctx).Where("status = ?", model.SourceStatusActive).Where("id NOT IN (SELECT source_id FROM knowledge_document WHERE review_status <> ?)", model.DocumentReviewIngested)
	if query.Kind != "" {
		db = db.Where("kind = ?", query.Kind)
	}
	if query.ExternalID != "" {
		db = db.Where("external_id = ?", query.ExternalID)
	}
	if query.Before != "" {
		db = db.Where("id < ?", query.Before)
	}
	sources := []model.Source{}
	err := db.Order("id DESC").Limit(query.Limit + 1).Find(&sources).Error
	return sources, err
}

func (s *SourceService) GetSource(ctx context.Context, sourceID string) (model.Source, error) {
	if !identifier.IsValid(sourceID, identifier.Source) {
		return model.Source{}, ErrInvalid
	}
	var source model.Source
	err := s.database.WithContext(ctx).First(&source, "id = ?", sourceID).Error
	return source, mapDatabaseError(err)
}

func (s *SourceService) ListSourceRecords(ctx context.Context, query SourceRecordQuery) ([]model.SourceRecord, error) {
	if query.Limit < 1 || query.Limit > 100 || (query.Before != "" && !identifier.IsValid(query.Before, identifier.SourceRecord)) {
		return nil, fmt.Errorf("%w: invalid limit or record cursor", ErrInvalid)
	}
	source, err := s.GetSource(ctx, query.SourceID)
	if err != nil {
		return nil, err
	}
	if source.Status != model.SourceStatusActive {
		return nil, ErrNotFound
	}
	db := s.database.WithContext(ctx).Where("source_id = ?", query.SourceID)
	if query.ExternalID != "" {
		db = db.Where("external_id = ?", query.ExternalID)
	}
	if query.Before != "" {
		db = db.Where("id < ?", query.Before)
	}
	records := []model.SourceRecord{}
	err = db.Omit("content").Order("id DESC").Limit(query.Limit + 1).Find(&records).Error
	return records, err
}
