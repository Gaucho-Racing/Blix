package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/depot"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *DocumentService) SetIntegration(storage *depot.Client, runtime *DocumentRuntimeClient) {
	s.depot = storage
	s.runtime = runtime
}

type DocumentUpload struct {
	Document model.Document `json:"document"`
	Upload   depot.Upload   `json:"upload"`
}

func (s *DocumentService) InitiateUpload(ctx context.Context, filename string, caller CallerContext) (DocumentUpload, error) {
	if s.depot == nil || !s.depot.Configured() || s.runtime == nil {
		return DocumentUpload{}, fmt.Errorf("%w: Depot integration is not configured", ErrUnavailable)
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || len(filename) > 512 || !utf8.ValidString(filename) || strings.ContainsAny(filename, "\x00\r\n") || !strings.EqualFold(filepath.Ext(filename), ".pdf") {
		return DocumentUpload{}, fmt.Errorf("%w: choose a PDF", ErrInvalid)
	}
	if !identifier.IsValid(caller.EntityID, identifier.Entity) || caller.EntityType != "USER" || !identifier.IsValid(caller.UserID, identifier.User) {
		return DocumentUpload{}, ErrForbidden
	}
	objective := "Prepare searchable text and visual evidence with PDF page citations, and identify the document title, publisher, edition, publication date, 2–3 useful tags and official source URL. Keep original evidence and interpretations distinct."
	title := strings.TrimSuffix(filename, filepath.Ext(filename))
	if title == "" {
		title = filename
	}
	callerData, _ := json.Marshal(map[string]string{"entity_id": caller.EntityID, "entity_type": caller.EntityType, "user_id": caller.UserID})
	doc := model.Document{ID: identifier.New(identifier.Document), Title: title, Filename: filename, Tags: datatypes.NewJSONSlice([]string{}), UploadedBy: caller.EntityID, Status: "uploading", Objective: objective, Caller: callerData, Generation: 1}
	setDocumentStage(&doc, "uploading", "running")
	if err := s.db.WithContext(ctx).Create(&doc).Error; err != nil {
		return DocumentUpload{}, err
	}
	upload, err := s.depot.Initiate(ctx, doc.Filename, doc.ID)
	if err != nil {
		setDocumentStage(&doc, "uploading", "failed")
		_ = saveDocumentProgress(s.db.WithContext(ctx).Where("review_status = ? AND status = ?", model.DocumentReviewPending, "uploading"), &doc, map[string]any{"status": "upload_failed", "error": err.Error()})
		return DocumentUpload{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	doc.DepotFileID = upload.File.ID
	if err := s.db.WithContext(ctx).Model(&doc).Update("depot_file_id", doc.DepotFileID).Error; err != nil {
		return DocumentUpload{}, err
	}
	return DocumentUpload{doc, upload}, nil
}
func (s *DocumentService) CompleteUpload(ctx context.Context, id, owner string) (DocumentDetails, error) {
	details, err := s.Get(ctx, id)
	if err != nil {
		return details, err
	}
	doc := details.Document
	if doc.ReviewStatus == model.DocumentReviewRejected {
		return DocumentDetails{}, ErrConflict
	}
	if doc.UploadedBy != owner {
		return DocumentDetails{}, ErrForbidden
	}
	if doc.Status != "uploading" {
		return details, nil
	}
	file, err := s.depot.Complete(ctx, doc.DepotFileID)
	if err != nil {
		return details, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if file.ID != doc.DepotFileID || file.Status != "ACTIVE" || file.SizeBytes < 5 || file.SizeBytes > MaxDocumentBytes {
		return details, fmt.Errorf("%w: upload a PDF up to 20 MiB", ErrInvalid)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		if current.ReviewStatus == model.DocumentReviewRejected {
			return nil
		}
		if current.ReviewStatus != model.DocumentReviewPending || current.Generation != doc.Generation {
			return ErrConflict
		}
		if current.Status != "uploading" {
			return nil
		}
		setDocumentStage(&current, "uploading", "completed")
		return saveDocumentProgress(tx, &current, map[string]any{"status": "queued", "size_bytes": file.SizeBytes, "error": ""})
	})
	if err != nil {
		return details, err
	}
	return s.Get(ctx, id)
}
func (s *DocumentService) Run(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.dispatchDocuments(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *DocumentService) dispatchDocuments(ctx context.Context) error {
	if s.runtime == nil {
		return nil
	}
	var docs []model.Document
	if err := s.db.WithContext(ctx).Where("status IN ?", documentProcessingStatuses).Order("created_at").Limit(100).Find(&docs).Error; err != nil {
		return err
	}
	for _, doc := range docs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if doc.TaskID == "" {
			if doc.Status != "queued" && doc.Status != "cancelling" {
				continue
			}
			var run struct {
				RunID            string `json:"run_id"`
				TaskID           string `json:"task_id"`
				ExtractionTaskID string `json:"extraction_task_id"`
			}
			request := map[string]any{"title": doc.Title, "key": fmt.Sprintf("%s:%d", doc.ID, doc.Generation), "document_id": doc.ID, "generation": doc.Generation, "caller": json.RawMessage(doc.Caller), "objective": doc.Objective}
			if err := s.runtime.request(ctx, "POST", "document-ingestions", request, &run); err != nil {
				_ = s.db.WithContext(ctx).Model(&model.Document{}).Where("id = ? AND generation = ? AND status = ?", doc.ID, doc.Generation, "queued").Update("error", "Waiting for runtime scheduling").Error
				continue
			}
			saved := s.db.WithContext(ctx).Model(&model.Document{}).Where("id = ? AND generation = ? AND task_id = '' AND review_status <> ?", doc.ID, doc.Generation, model.DocumentReviewRejected).Updates(map[string]any{"task_id": run.TaskID, "extraction_task_id": run.ExtractionTaskID, "run_id": run.RunID, "error": ""})
			if saved.Error != nil {
				return saved.Error
			}
			if saved.RowsAffected == 0 {
				var current model.Document
				if err := s.db.WithContext(ctx).First(&current, "id = ?", doc.ID).Error; err != nil {
					return err
				}
				if current.TaskID != run.TaskID {
					if current.ReviewStatus == model.DocumentReviewRejected && current.TaskID == "" {
						if err := s.db.WithContext(ctx).Model(&model.Document{}).Where("id = ? AND generation = ? AND task_id = '' AND review_status = ?", current.ID, current.Generation, model.DocumentReviewRejected).Updates(map[string]any{"task_id": run.TaskID, "status": "cancelling"}).Error; err != nil {
							return err
						}
					}
					if err := s.runtime.control(ctx, run.TaskID, doc.UploadedBy, "cancel"); err != nil && !errors.Is(err, ErrConflict) {
						return err
					}
				}
			}
		} else {
			var task struct {
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			}
			if doc.Status == "cancelling" {
				if err := s.runtime.control(ctx, doc.TaskID, doc.UploadedBy, "cancel"); err != nil && !errors.Is(err, ErrConflict) {
					continue
				}
			}
			if err := s.runtime.request(ctx, "GET", "tasks/"+doc.TaskID, nil, &task); err != nil {
				continue
			}
			status := ""
			message := ""
			switch task.Status {
			case "failed", "expired":
				status = "failed"
				message = "Document processing failed. Retry to continue from saved findings."
				var failure struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(task.Error, &failure) == nil && failure.Message != "" && len(failure.Message) < 4000 {
					message = failure.Message
				}
			case "cancelled":
				status = "cancelled"
			case "succeeded":
				status = "failed"
				message = "The executor finished without completing document ingestion."
			}
			if status != "" {
				if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var current model.Document
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", doc.ID).Error; err != nil {
						return err
					}
					if current.Generation != doc.Generation || current.TaskID != doc.TaskID || !documentIsProcessing(current.Status) {
						return nil
					}
					stopDocumentStage(&current, status)
					return saveDocumentProgress(tx, &current, map[string]any{"status": status, "error": message})
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *DocumentService) Control(ctx context.Context, id, owner, operation string) (DocumentDetails, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&doc, "id = ?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		if doc.ReviewStatus == model.DocumentReviewRejected {
			return ErrConflict
		}
		if doc.UploadedBy != owner {
			return ErrForbidden
		}
		switch operation {
		case "cancel":
			if doc.Status == "cancelled" || doc.Status == "cancelling" {
				return nil
			}
			if !documentIsProcessing(doc.Status) {
				return ErrConflict
			}
			return tx.Model(&doc).Update("status", "cancelling").Error
		case "retry":
			if doc.Status != "failed" && doc.Status != "cancelled" {
				return ErrConflict
			}
			resetUnfinishedDocumentStages(&doc)
			return saveDocumentProgress(tx, &doc, map[string]any{"status": "queued", "generation": doc.Generation + 1, "task_id": "", "session_id": "", "attempt_number": 0, "extraction_task_id": "", "extraction_attempt_number": 0, "run_id": "", "error": ""})
		default:
			return ErrInvalid
		}
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}

type DocumentEvidence struct {
	Page           int    `json:"page"`
	SourceRecordID string `json:"source_record_id"`
	Offset         int    `json:"offset"`
	Content        string `json:"content"`
}

func buildDocumentChunks(id string, pages []model.DocumentPage, text []string) []model.DocumentChunk {
	chunks := []model.DocumentChunk{}
	pieces := []DocumentEvidence{}
	size := 0
	flush := func() {
		if len(pieces) == 0 {
			return
		}
		data, _ := json.Marshal(pieces)
		chunks = append(chunks, model.DocumentChunk{DocumentID: id, Number: len(chunks) + 1, FirstPage: pieces[0].Page, LastPage: pieces[len(pieces)-1].Page, Evidence: data})
		pieces = nil
		size = 0
	}
	for i, page := range pages {
		content := text[i]
		offset := 0
		for {
			if len(pieces) > 0 && (size+len(content) > 16000 || page.Number-pieces[0].Page >= 4) {
				flush()
			}
			end := min(len(content), 16000)
			for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
				end--
			}
			pieces = append(pieces, DocumentEvidence{page.Number, page.SourceRecordID, offset, content[:end]})
			size += end
			offset += end
			content = content[end:]
			if content == "" {
				break
			}
			flush()
		}
	}
	flush()
	return chunks
}
func (s *DocumentService) lockedExecution(ctx context.Context, tx *gorm.DB, id string, e DocumentExecution) (model.Document, error) {
	var doc model.Document
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&doc, "id = ?", id).Error; err != nil {
		return doc, mapDatabaseError(err)
	}
	if doc.ReviewStatus == model.DocumentReviewRejected || doc.Generation != e.Generation || !documentIsProcessing(doc.Status) || doc.Status == "cancelling" {
		return doc, ErrConflict
	}
	access, err := s.runtime.validate(ctx, id, e)
	if err != nil {
		return doc, err
	}
	if access.ProfileID != "document_ingest_v1" {
		return doc, ErrForbidden
	}
	if doc.TaskID != "" && doc.TaskID != access.TaskID {
		return doc, ErrConflict
	}
	if doc.AttemptNumber > access.AttemptNumber {
		return doc, ErrConflict
	}
	doc.TaskID = access.TaskID
	doc.SessionID = access.SessionID
	doc.RunID = access.RunID
	doc.AttemptNumber = access.AttemptNumber
	return doc, tx.Model(&doc).Updates(map[string]any{"task_id": doc.TaskID, "session_id": doc.SessionID, "run_id": doc.RunID, "attempt_number": doc.AttemptNumber}).Error
}
func (s *DocumentService) Prepare(ctx context.Context, id string, e DocumentExecution) (DocumentDetails, error) {
	details, err := s.Get(ctx, id)
	if err != nil {
		return details, err
	}
	prepared := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.DocumentChunk{}).Where("document_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			prepared = true
			return nil
		}
		return startDocumentStage(tx, &doc, "extracting")
	})
	if err != nil {
		return details, err
	}
	if prepared {
		return s.prepareSearch(ctx, id, e)
	}
	if s.extractor == nil {
		return details, fmt.Errorf("%w: file extraction has not completed", ErrConflict)
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return DocumentDetails{}, ctx.Err()
	}
	var data []byte
	if details.Document.DepotFileID != "" {
		data, err = s.depot.Download(ctx, details.Document.DepotFileID, MaxDocumentBytes)
	} else {
		data, err = os.ReadFile(filepath.Join(s.directory, details.Document.SHA256+".pdf"))
	}
	if err != nil {
		return details, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(data) > MaxDocumentBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return details, fmt.Errorf("%w: stored file is not a PDF within the size limit", ErrInvalid)
	}
	hash := hashBytes(data)
	if details.Document.SHA256 != "" && details.Document.SHA256 != hash {
		return details, fmt.Errorf("%w: the stored PDF changed", ErrConflict)
	}
	temp, err := os.CreateTemp("", "blix-extract-*.pdf")
	if err != nil {
		return details, err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return details, err
	}
	if err := temp.Close(); err != nil {
		return details, err
	}
	text, err := s.extractor.Extract(ctx, temp.Name())
	if err != nil {
		return details, err
	}
	text, err = s.preparePageImages(ctx, details.Document, hash, temp.Name(), text, e)
	if err != nil {
		return details, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.DocumentChunk{}).Where("document_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		return s.saveExtractedDocument(ctx, tx, doc, hash, int64(len(data)), text)
	})
	if err != nil {
		return details, err
	}
	return s.prepareSearch(ctx, id, e)
}

func (s *DocumentService) prepareSearch(ctx context.Context, id string, e DocumentExecution) (DocumentDetails, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		return startDocumentStage(tx, &doc, "indexing")
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		if !documentStageComplete(doc, "indexing") {
			var records []model.SourceRecord
			if err := tx.Where("id IN (SELECT source_record_id FROM knowledge_document_page WHERE document_id = ?)", id).Find(&records).Error; err != nil {
				return err
			}
			if len(records) == 0 {
				return ErrConflict
			}
			for _, record := range records {
				if err := indexSourceRecord(tx, record); err != nil {
					return err
				}
			}
			setDocumentStage(&doc, "indexing", "completed")
		}
		return advanceDocumentEnrichment(tx, &doc)
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}
func (s *DocumentService) ReadChunk(ctx context.Context, id string, number int) (map[string]any, error) {
	var chunk model.DocumentChunk
	if err := s.db.WithContext(ctx).First(&chunk, "document_id = ? AND number = ?", id, number).Error; err != nil {
		return nil, mapDatabaseError(err)
	}
	var evidence []DocumentEvidence
	if err := json.Unmarshal(chunk.Evidence, &evidence); err != nil {
		return nil, err
	}
	images, err := s.ChunkImages(ctx, id, chunk.FirstPage, chunk.LastPage)
	if err != nil {
		return nil, err
	}
	visualEvidence, err := s.chunkVisualEvidence(ctx, id, chunk.FirstPage, chunk.LastPage)
	if err != nil {
		return nil, err
	}
	return map[string]any{"chunk": chunk, "evidence": evidence, "images": images, "visual_evidence": visualEvidence}, nil
}

type VisualFinding struct {
	Page        int      `json:"page"`
	Description string   `json:"description"`
	SectionIDs  []string `json:"section_ids"`
}

func (s *DocumentService) Finish(ctx context.Context, id string, e DocumentExecution) (DocumentDetails, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		for _, stage := range documentStageNames {
			if !documentStageComplete(doc, stage) {
				return ErrConflict
			}
		}
		if len(doc.ExtractedMetadata) == 0 || doc.PageCount == 0 {
			return ErrConflict
		}
		var lowText int64
		if err := tx.Model(&model.DocumentPage{}).Where("document_id = ? AND character_count < 20 AND visual_source_record_id = ''", id).Count(&lowText).Error; err != nil {
			return err
		}
		status, message := "ready", ""
		if lowText > 0 {
			status = "partial"
			message = fmt.Sprintf("%d pages have little or no readable text after extraction and OCR. See their saved visual findings for available evidence.", lowText)
		}
		updates := map[string]any{"status": status, "error": message}
		if doc.ReviewStatus == model.DocumentReviewPending {
			updates["review_status"] = model.DocumentReviewInReview
		}
		return saveDocumentProgress(tx, &doc, updates)
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}

func (s *DocumentService) ReadOriginal(ctx context.Context, id string) ([]byte, string, error) {
	details, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	doc := details.Document
	var data []byte
	if doc.DepotFileID != "" {
		data, err = s.depot.Download(ctx, doc.DepotFileID, MaxDocumentBytes)
	} else {
		data, err = os.ReadFile(filepath.Join(s.directory, doc.SHA256+".pdf"))
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: original PDF is unavailable", ErrUnavailable)
	}
	if doc.SHA256 != "" && hashBytes(data) != doc.SHA256 {
		return nil, "", fmt.Errorf("%w: stored PDF differs from the ingested evidence", ErrConflict)
	}
	return data, doc.Filename, nil
}

var documentProcessingStatuses = []string{"queued", "extracting", "indexing", "analyzing_visuals", "reading", "identifying_metadata", "reconciling", "cancelling"}

func documentIsProcessing(status string) bool {
	return slices.Contains(documentProcessingStatuses, status)
}

func (s *DocumentService) saveExtractedDocument(ctx context.Context, tx *gorm.DB, doc model.Document, hash string, size int64, text []string) error {
	id := doc.ID
	sources := NewSourceService(tx)
	sourceData, _ := json.Marshal(map[string]any{"document_id": id, "sha256": hash, "filename": doc.Filename, "metadata_status": "unidentified"})
	source, err := sources.CreateSource(ctx, CreateSourceInput{Kind: "pdf_document", ExternalID: id, Name: doc.Title, Metadata: sourceData})
	if err != nil {
		return err
	}
	pages := []model.DocumentPage{}
	for i, content := range text {
		pageMetadata := map[string]any{"document_id": id, "page_number": i + 1, "filename": doc.Filename, "metadata_status": "unidentified", "published_at": doc.PublishedAt, "sha256": hash}
		method := "document_upload"
		imageHash := ""
		var image model.DocumentPageImage
		if err := tx.First(&image, "document_id = ? AND number = ?", id, i+1).Error; err == nil {
			imageHash = image.SHA256
			pageMetadata["page_image_sha256"] = image.SHA256
			pageMetadata["text_method"] = image.TextMethod
			if image.TextMethod == "ocr" {
				method = "document_ocr"
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		metadata, _ := json.Marshal(pageMetadata)
		record, err := sources.ingestSourceRecord(ctx, source.ID, IngestSourceRecordInput{ExternalID: fmt.Sprintf("page:%d", i+1), ExternalVersion: hash, RecordType: "pdf_page", Content: content, Metadata: metadata, ExternalURL: doc.ExternalURL, OccurredAt: doc.CreatedAt, EvidenceType: "quoted_source", CaptureMethod: method}, false)
		if err != nil {
			return err
		}
		pages = append(pages, model.DocumentPage{DocumentID: id, Number: i + 1, SourceRecordID: record.Record.ID, ImageSHA256: imageHash, CharacterCount: utf8.RuneCountInString(content)})
	}
	if err := tx.Create(&pages).Error; err != nil {
		return err
	}
	chunks := buildDocumentChunks(id, pages, text)
	if err := tx.Create(&chunks).Error; err != nil {
		return err
	}
	setDocumentStage(&doc, "extracting", "completed")
	setDocumentStage(&doc, "indexing", "running")
	return saveDocumentProgress(tx, &doc, map[string]any{"sha256": hash, "source_id": source.ID, "page_count": len(pages), "size_bytes": size, "status": "indexing", "error": ""})
}
