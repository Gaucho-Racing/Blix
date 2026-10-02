package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/gorm"
)

func equalVisualFindings(left, right []byte) bool {
	var a, b []VisualFinding
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func saveVisualFindings(ctx context.Context, tx *gorm.DB, doc model.Document, chunk model.DocumentChunk, findings []VisualFinding) error {
	seen := map[int]bool{}
	for _, finding := range findings {
		if seen[finding.Page] || finding.Page < chunk.FirstPage || finding.Page > chunk.LastPage || strings.TrimSpace(finding.Description) == "" || len(finding.Description) > 8000 || !utf8.ValidString(finding.Description) || strings.ContainsRune(finding.Description, 0) || len(finding.SectionIDs) > 20 {
			return ErrInvalid
		}
		seen[finding.Page] = true
		for _, section := range finding.SectionIDs {
			if section == "" || ruleIdentifier.FindString(section) != section {
				return fmt.Errorf("%w: visual section IDs must be exact rule identifiers", ErrInvalid)
			}
		}
		var asset model.DocumentPageImage
		if err := tx.First(&asset, "document_id = ? AND number = ?", doc.ID, finding.Page).Error; err != nil {
			return mapDatabaseError(err)
		}
		var page model.DocumentPage
		if err := tx.First(&page, "document_id = ? AND number = ?", doc.ID, finding.Page).Error; err != nil {
			return mapDatabaseError(err)
		}
		for _, sourceID := range sourceRecordReferencePattern.FindAllString(finding.Description, -1) {
			if sourceID != page.SourceRecordID {
				return ErrInvalid
			}
		}
		content := fmt.Sprintf("PDF page %d visual interpretation. Sections: %s\n%s", finding.Page, strings.Join(finding.SectionIDs, ", "), finding.Description)
		if page.VisualSourceRecordID != "" {
			var previous model.SourceRecord
			if err := tx.First(&previous, "id = ?", page.VisualSourceRecordID).Error; err != nil {
				return err
			}
			content = previous.Content + "\n\n" + content
		}
		metadata, err := json.Marshal(map[string]any{"document_id": doc.ID, "page_number": finding.Page, "original_source_record_id": page.SourceRecordID, "page_image_sha256": asset.SHA256, "section_ids": finding.SectionIDs, "reading_chunk": chunk.Number})
		if err != nil {
			return err
		}
		record, err := NewSourceService(tx).ingestSourceRecord(ctx, doc.SourceID, IngestSourceRecordInput{
			ExternalID: fmt.Sprintf("page:%d:visual", finding.Page), ExternalVersion: hashBytes([]byte(content)), ParentExternalID: fmt.Sprintf("page:%d", finding.Page), RecordType: "pdf_page_visual", Content: content, Metadata: metadata, OccurredAt: doc.CreatedAt, EvidenceType: "agent_summary", CaptureMethod: "document_vision", CaptureConversationID: doc.ConversationID, CaptureSessionID: doc.SessionID,
		}, false)
		if err != nil {
			return err
		}
		if err := indexSourceRecord(tx, record.Record); err != nil {
			return err
		}
		if err := tx.Model(&page).Update("visual_source_record_id", record.Record.ID).Error; err != nil {
			return err
		}
	}
	return nil
}
