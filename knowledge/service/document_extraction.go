package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ExtractionManifest struct {
	MediaType      string `json:"media_type"`
	Filename       string `json:"filename"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	DepotFileID    string `json:"depot_file_id"`
	PageCount      int    `json:"page_count"`
	CompletedPages []int  `json:"completed_pages"`
	Version        int    `json:"version"`
}
type ExtractionStart struct {
	DocumentExecution
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	PageCount int    `json:"page_count"`
	Version   int    `json:"version"`
}
type ExtractionPage struct {
	DocumentExecution
	NativeText  string `json:"native_text"`
	OCRText     string `json:"ocr_text"`
	Text        string `json:"text"`
	TextMethod  string `json:"text_method"`
	SHA256      string `json:"sha256"`
	DepotFileID string `json:"depot_file_id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ImageBase64 string `json:"image_base64,omitempty"`
}

func (s *DocumentService) lockedExtraction(ctx context.Context, tx *gorm.DB, id string, execution DocumentExecution) (model.Document, error) {
	var doc model.Document
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&doc, "id = ?", id).Error; err != nil {
		return doc, mapDatabaseError(err)
	}
	if s.runtime == nil {
		return doc, ErrUnavailable
	}
	if doc.Generation != execution.Generation || doc.ReviewStatus == model.DocumentReviewRejected || !documentIsProcessing(doc.Status) || doc.Status == "cancelling" {
		return doc, ErrConflict
	}
	access, err := s.runtime.validate(ctx, id, execution)
	if err != nil {
		return doc, err
	}
	if access.ProfileID != "file_ingest_v1" || access.ParentTaskID == nil || *access.ParentTaskID == "" {
		return doc, ErrForbidden
	}
	if (doc.TaskID != "" && doc.TaskID != *access.ParentTaskID) || (doc.ExtractionTaskID != "" && doc.ExtractionTaskID != access.TaskID) || (doc.RunID != "" && doc.RunID != access.RunID) || doc.ExtractionAttemptNumber > access.AttemptNumber {
		return doc, ErrConflict
	}
	doc.TaskID = *access.ParentTaskID
	doc.ExtractionTaskID = access.TaskID
	doc.RunID = access.RunID
	doc.ExtractionAttemptNumber = access.AttemptNumber
	return doc, tx.Model(&doc).Updates(map[string]any{"task_id": doc.TaskID, "extraction_task_id": doc.ExtractionTaskID, "run_id": doc.RunID, "extraction_attempt_number": doc.ExtractionAttemptNumber}).Error
}

func (s *DocumentService) ExtractionManifest(ctx context.Context, id string, execution DocumentExecution) (ExtractionManifest, error) {
	var manifest ExtractionManifest
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExtraction(ctx, tx, id, execution)
		if err != nil {
			return err
		}
		if err := startDocumentStage(tx, &doc, "extracting"); err != nil {
			return err
		}
		manifest = ExtractionManifest{MediaType: "application/pdf", Filename: doc.Filename, SHA256: doc.SHA256, SizeBytes: doc.SizeBytes, DepotFileID: doc.DepotFileID, PageCount: doc.PageCount, Version: pageImageVersion, CompletedPages: []int{}}
		return tx.Model(&model.DocumentPageImage{}).Where("document_id = ? AND pdf_hash = ? AND version = ?", id, doc.SHA256, pageImageVersion).Order("number").Pluck("number", &manifest.CompletedPages).Error
	})
	return manifest, err
}
func (s *DocumentService) ExtractionFile(ctx context.Context, id string, execution DocumentExecution) ([]byte, error) {
	if _, err := s.ExtractionManifest(ctx, id, execution); err != nil {
		return nil, err
	}
	data, _, err := s.ReadOriginal(ctx, id)
	return data, err
}
func validSHA256(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && value == fmt.Sprintf("%x", data)
}
func (s *DocumentService) StartExtraction(ctx context.Context, id string, request ExtractionStart) error {
	if !validSHA256(request.SHA256) || request.SizeBytes < 5 || request.SizeBytes > MaxDocumentBytes || request.PageCount < 1 || request.PageCount > 500 || request.Version != pageImageVersion {
		return ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExtraction(ctx, tx, id, request.DocumentExecution)
		if err != nil {
			return err
		}
		if (doc.SHA256 != "" && doc.SHA256 != request.SHA256) || (doc.SizeBytes != 0 && doc.SizeBytes != request.SizeBytes) || (doc.PageCount != 0 && doc.PageCount != request.PageCount) {
			return ErrConflict
		}
		if documentStageComplete(doc, "extracting") {
			return nil
		}
		setDocumentStage(&doc, "extracting", "running")
		return saveDocumentProgress(tx, &doc, map[string]any{"sha256": request.SHA256, "size_bytes": request.SizeBytes, "page_count": request.PageCount, "status": "extracting", "error": ""})
	})
}
func (s *DocumentService) SaveExtractionPage(ctx context.Context, id string, number int, request ExtractionPage) error {
	if number < 1 || number > 500 || !validSHA256(request.SHA256) || request.Width < 1 || request.Height < 1 || request.Width > 2400 || request.Height > 2400 {
		return ErrInvalid
	}
	for _, text := range []string{request.Text, request.NativeText, request.OCRText} {
		if len(text) > 1024*1024 || !utf8.ValidString(text) || bytes.ContainsRune([]byte(text), 0) {
			return ErrInvalid
		}
	}
	if (request.TextMethod != "native_text" && request.TextMethod != "ocr") || (request.TextMethod == "native_text" && request.Text != request.NativeText) || (request.TextMethod == "ocr" && request.Text != request.OCRText) {
		return ErrInvalid
	}
	if (request.DepotFileID == "") == (request.ImageBase64 == "") {
		return ErrInvalid
	}
	var image []byte
	if request.ImageBase64 != "" {
		var err error
		image, err = base64.StdEncoding.DecodeString(request.ImageBase64)
		if err != nil || len(image) > maxPageImageBytes || hashBytes(image) != request.SHA256 {
			return ErrInvalid
		}
		config, err := jpeg.DecodeConfig(bytes.NewReader(image))
		if err != nil || config.Width != request.Width || config.Height != request.Height {
			return ErrInvalid
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExtraction(ctx, tx, id, request.DocumentExecution)
		if err != nil {
			return err
		}
		if number > doc.PageCount || doc.SHA256 == "" {
			return ErrConflict
		}
		var existing model.DocumentPageImage
		if err := tx.First(&existing, "document_id = ? AND number = ?", id, number).Error; err == nil {
			if existing.PDFHash != doc.SHA256 || existing.Version != pageImageVersion || existing.SHA256 != request.SHA256 || existing.Text != request.Text || existing.NativeText != request.NativeText || existing.OCRText != request.OCRText {
				return ErrConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if len(image) > 0 {
			dir := filepath.Join(s.directory, "pages")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, request.SHA256+".jpg"), image, 0600); err != nil {
				return err
			}
		}
		asset := model.DocumentPageImage{DocumentID: id, Number: number, PDFHash: doc.SHA256, Version: pageImageVersion, SHA256: request.SHA256, DepotFileID: request.DepotFileID, Width: request.Width, Height: request.Height, NativeText: request.NativeText, OCRText: request.OCRText, Text: request.Text, TextMethod: request.TextMethod}
		return tx.Create(&asset).Error
	})
}
func (s *DocumentService) CompleteExtraction(ctx context.Context, id string, execution DocumentExecution) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExtraction(ctx, tx, id, execution)
		if err != nil {
			return err
		}
		if documentStageComplete(doc, "extracting") {
			return nil
		}
		var assets []model.DocumentPageImage
		if err := tx.Where("document_id = ?", id).Order("number").Find(&assets).Error; err != nil {
			return err
		}
		if doc.PageCount < 1 || len(assets) != doc.PageCount {
			return ErrConflict
		}
		text := make([]string, len(assets))
		total := 0
		for i, asset := range assets {
			if asset.Number != i+1 || asset.PDFHash != doc.SHA256 || asset.Version != pageImageVersion {
				return ErrConflict
			}
			total += len(asset.Text)
			if total > 10*1024*1024 {
				return ErrInvalid
			}
			text[i] = asset.Text
		}
		return s.saveExtractedDocument(ctx, tx, doc, doc.SHA256, doc.SizeBytes, text)
	})
}
