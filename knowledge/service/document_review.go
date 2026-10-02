package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func documentChangesets(tx *gorm.DB, doc model.Document) *gorm.DB {
	query := tx.Where("id IN (SELECT changeset_id FROM knowledge_changeset_document WHERE document_id = ?) OR id IN (SELECT changeset_id FROM knowledge_changeset_source_record JOIN knowledge_document_page ON knowledge_document_page.source_record_id = knowledge_changeset_source_record.source_record_id WHERE document_id = ?)", doc.ID, doc.ID)
	if doc.SessionID != "" {
		query = query.Or("session_id = ? AND conversation_id = ?", doc.SessionID, doc.ConversationID)
	}
	return query
}

func changesetDocuments(tx *gorm.DB, id string) *gorm.DB {
	return tx.Where(`id IN (SELECT document_id FROM knowledge_changeset_document WHERE changeset_id = ?) OR id IN (SELECT document_id FROM knowledge_document_page JOIN knowledge_changeset_source_record ON knowledge_document_page.source_record_id = knowledge_changeset_source_record.source_record_id WHERE changeset_id = ?)
		OR (session_id <> '' AND (session_id, conversation_id) IN (SELECT session_id, conversation_id FROM knowledge_changeset WHERE id = ?))`, id, id, id)
}

func lockChangesetDocuments(tx *gorm.DB, id string) ([]model.Document, error) {
	docs := []model.Document{}
	err := changesetDocuments(tx, id).Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&docs).Error
	return docs, err
}

func documentProcessingComplete(status string) bool {
	return status == "ready" || status == "partial" || status == "no_changes" || status == "imported"
}

func documentPublicationStarted(changeset model.Changeset) bool {
	return changeset.Status == model.ChangesetStatusCommitted || changeset.Status == model.ChangesetStatusPrepared || changeset.ResultCommitSHA != "" ||
		(changeset.PublicationLeaseUntil != nil && changeset.PublicationLeaseUntil.After(time.Now()))
}

func documentApprovalUpdates(input ReviewInput) map[string]any {
	return map[string]any{"review_status": model.DocumentReviewIngested, "reviewed_by": input.Caller.EntityID, "review_note": strings.TrimSpace(input.Note), "reviewed_at": time.Now().UTC()}
}

func (s *DocumentService) Review(ctx context.Context, id, decision string, input ReviewInput) (DocumentDetails, error) {
	if err := validateReviewInput(input, decision); err != nil {
		return DocumentDetails{}, err
	}
	if !identifier.IsValid(id, identifier.Document) {
		return DocumentDetails{}, ErrInvalid
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&doc, "id = ?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		if (decision == "approve" && doc.ReviewStatus == model.DocumentReviewIngested) || (decision == "reject" && doc.ReviewStatus == model.DocumentReviewRejected) {
			return nil
		}
		if doc.ReviewStatus != model.DocumentReviewInReview {
			return fmt.Errorf("%w: document review is already complete", ErrConflict)
		}
		if decision == "approve" {
			if !documentProcessingComplete(doc.Status) {
				return fmt.Errorf("%w: document processing must finish before approval", ErrConflict)
			}
			return tx.Model(&doc).Updates(documentApprovalUpdates(input)).Error
		}
		var changesets []model.Changeset
		if err := documentChangesets(tx, doc).Order("id").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&changesets).Error; err != nil {
			return err
		}
		for _, changeset := range changesets {
			if documentPublicationStarted(changeset) {
				return fmt.Errorf("%w: linked changeset publication has started; resolve its outcome before rejecting the document", ErrConflict)
			}
		}
		for _, changeset := range changesets {
			if changeset.Status == model.ChangesetStatusRejected {
				continue
			}
			updates := reviewUpdates(input, "rejected")
			updates["status"] = model.ChangesetStatusRejected
			updates["rejected_document_id"] = doc.ID
			if err := tx.Model(&changeset).Updates(updates).Error; err != nil {
				return err
			}
		}
		if doc.SourceID != "" {
			if err := tx.Model(&model.Source{}).Where("id = ?", doc.SourceID).Update("status", model.SourceStatusDisabled).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"review_status": model.DocumentReviewRejected, "reviewed_by": input.Caller.EntityID, "review_note": strings.TrimSpace(input.Note), "reviewed_at": time.Now().UTC(), "generation": doc.Generation + 1}
		if documentIsProcessing(doc.Status) || doc.Status == "uploading" {
			updates["status"] = "cancelled"
			if doc.TaskID != "" {
				updates["status"] = "cancelling"
			}
		}
		return tx.Model(&doc).Updates(updates).Error
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}
