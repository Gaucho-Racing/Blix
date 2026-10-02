package service

import (
	"context"
	"slices"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func (s *DocumentService) ValidateQuestionDocument(ctx context.Context, id string) error {
	if !identifier.IsValid(id, identifier.Document) {
		return ErrInvalid
	}
	documents, err := retrievalDocuments(s.db.WithContext(ctx), DocumentRetrievalScope{DocumentIDs: []string{id}, AllowUnapproved: true}, id)
	if err != nil {
		return err
	}
	if len(documents) != 1 {
		return ErrNotFound
	}
	if !documents[0].ReadyForQuestions {
		return ErrConflict
	}
	return nil
}

func (s *DocumentService) AuthorizeAgentDocument(ctx context.Context, id, conversationID, sessionID string) error {
	if !identifier.IsValid(id, identifier.Document) {
		return ErrInvalid
	}
	var doc model.Document
	if err := s.db.WithContext(ctx).First(&doc, "id = ?", id).Error; err != nil {
		return mapDatabaseError(err)
	}
	if doc.ReviewStatus == model.DocumentReviewRejected {
		return ErrNotFound
	}
	if doc.ReviewStatus == model.DocumentReviewIngested {
		return nil
	}
	access, err := s.agentDocumentContext(ctx, conversationID, sessionID)
	if err != nil {
		return err
	}
	if access.ProfileID != "document_ingest_v1" || !slices.Contains(access.DocumentIDs, id) {
		return ErrForbidden
	}
	return nil
}

func (s *DocumentService) AuthorizeAgentSource(ctx context.Context, sourceID, conversationID, sessionID string) error {
	if !identifier.IsValid(sourceID, identifier.Source) {
		return ErrInvalid
	}
	var documents []model.Document
	if err := s.db.WithContext(ctx).Where("source_id = ?", sourceID).Find(&documents).Error; err != nil {
		return err
	}
	for _, doc := range documents {
		if err := s.AuthorizeAgentDocument(ctx, doc.ID, conversationID, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *DocumentService) AuthorizeAgentRecord(ctx context.Context, recordID, conversationID, sessionID string) error {
	if !identifier.IsValid(recordID, identifier.SourceRecord) {
		return ErrInvalid
	}
	var record model.SourceRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", recordID).Error; err != nil {
		return mapDatabaseError(err)
	}
	return s.AuthorizeAgentSource(ctx, record.SourceID, conversationID, sessionID)
}
