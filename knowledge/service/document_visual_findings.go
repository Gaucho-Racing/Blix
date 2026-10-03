package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var visualSectionIdentifier = regexp.MustCompile(`^[A-Z]{1,4}\.[0-9]+(?:\.[0-9]+)*(?:\.[a-zA-Z])?$`)

type SaveDocumentVisualAnalysis struct {
	DocumentExecution
	ImageSHA256  string         `json:"image_sha256"`
	Finding      *VisualFinding `json:"finding"`
	Model        string         `json:"model"`
	InputTokens  uint64         `json:"input_tokens"`
	OutputTokens uint64         `json:"output_tokens"`
}

func equalVisualAnalysis(left, right []byte) bool {
	var a, b *VisualFinding
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func (s *DocumentService) SaveVisualAnalysis(ctx context.Context, id string, number int, r SaveDocumentVisualAnalysis) (model.DocumentPage, error) {
	var page model.DocumentPage
	if number < 1 || number > 500 || strings.TrimSpace(r.Model) == "" || len(r.Model) > 128 || !utf8.ValidString(r.Model) || strings.ContainsAny(r.Model, "\x00\r\n") {
		return page, ErrInvalid
	}
	data, err := json.Marshal(r.Finding)
	if err != nil {
		return page, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, r.DocumentExecution)
		if err != nil {
			return err
		}
		if !documentStageComplete(doc, "indexing") {
			return ErrConflict
		}
		if err := tx.First(&page, "document_id = ? AND number = ?", id, number).Error; err != nil {
			return mapDatabaseError(err)
		}
		var image model.DocumentPageImage
		if err := tx.First(&image, "document_id = ? AND number = ?", id, number).Error; err != nil {
			return mapDatabaseError(err)
		}
		if image.SHA256 != r.ImageSHA256 || page.ImageSHA256 != r.ImageSHA256 {
			return ErrConflict
		}
		if page.VisualAnalyzedAt != nil {
			if !equalVisualAnalysis(page.VisualAnalysis, data) || page.VisualModel != r.Model || page.VisualInputTokens != r.InputTokens || page.VisualOutputTokens != r.OutputTokens {
				return ErrConflict
			}
			return nil
		}
		if r.Finding != nil {
			if err := saveVisualFinding(ctx, tx, doc, &page, image, *r.Finding, r.Model); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		page.VisualAnalyzedAt = &now
		page.VisualAnalysis = datatypes.JSON(data)
		page.VisualModel = r.Model
		page.VisualInputTokens, page.VisualOutputTokens = r.InputTokens, r.OutputTokens
		if err := tx.Save(&page).Error; err != nil {
			return err
		}
		return advanceDocumentEnrichment(tx, &doc)
	})
	return page, err
}

func saveVisualFinding(ctx context.Context, tx *gorm.DB, doc model.Document, page *model.DocumentPage, image model.DocumentPageImage, finding VisualFinding, modelName string) error {
	if finding.Page != page.Number || strings.TrimSpace(finding.Description) == "" || len(finding.Description) > 8000 || !utf8.ValidString(finding.Description) || strings.ContainsRune(finding.Description, 0) || len(finding.SectionIDs) > 20 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, section := range finding.SectionIDs {
		if seen[section] || !visualSectionIdentifier.MatchString(section) {
			return fmt.Errorf("%w: visual section IDs must be unique exact rule identifiers", ErrInvalid)
		}
		seen[section] = true
	}
	for _, sourceID := range sourceRecordReferencePattern.FindAllString(finding.Description, -1) {
		if sourceID != page.SourceRecordID {
			return ErrInvalid
		}
	}
	metadata, err := json.Marshal(map[string]any{"document_id": doc.ID, "page_number": finding.Page, "original_source_record_id": page.SourceRecordID, "page_image_sha256": image.SHA256, "section_ids": finding.SectionIDs, "model": modelName})
	if err != nil {
		return err
	}
	content := fmt.Sprintf("PDF page %d visual interpretation. Sections: %s\n%s", finding.Page, strings.Join(finding.SectionIDs, ", "), finding.Description)
	record, err := NewSourceService(tx).ingestSourceRecord(ctx, doc.SourceID, IngestSourceRecordInput{
		ExternalID: fmt.Sprintf("page:%d:visual", finding.Page), ExternalVersion: hashBytes([]byte(content)), ParentExternalID: fmt.Sprintf("page:%d", finding.Page), RecordType: "pdf_page_visual", Content: content, Metadata: metadata, OccurredAt: doc.CreatedAt, EvidenceType: "agent_summary", CaptureMethod: "document_vision", CaptureRunID: doc.RunID, CaptureSessionID: doc.SessionID,
	}, false)
	if err != nil {
		return err
	}
	if err := indexSourceRecord(tx, record.Record); err != nil {
		return err
	}
	page.VisualSourceRecordID = record.Record.ID
	return nil
}

func advanceDocumentEnrichment(tx *gorm.DB, doc *model.Document) error {
	if len(doc.ExtractedMetadata) == 0 {
		setDocumentStage(doc, "identifying_metadata", "running")
	}
	setDocumentStage(doc, "analyzing_visuals", "running")
	var pending int64
	if err := tx.Model(&model.DocumentPage{}).Where("document_id = ? AND image_sha256 <> '' AND visual_analyzed_at IS NULL", doc.ID).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return startDocumentStage(tx, doc, "analyzing_visuals")
	}
	setDocumentStage(doc, "analyzing_visuals", "completed")
	if len(doc.ExtractedMetadata) == 0 {
		return startDocumentStage(tx, doc, "identifying_metadata")
	}
	return saveDocumentProgress(tx, doc, map[string]any{})
}

func (s *DocumentService) chunkVisualEvidence(ctx context.Context, id string, first, last int) ([]model.SourceRecord, error) {
	records := []model.SourceRecord{}
	err := s.db.WithContext(ctx).Where("id IN (SELECT visual_source_record_id FROM knowledge_document_page WHERE document_id = ? AND number BETWEEN ? AND ?)", id, first, last).Order("external_id").Find(&records).Error
	return records, err
}
