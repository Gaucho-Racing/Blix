package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ExtractedDocumentMetadata struct {
	Title           string   `json:"title"`
	Publisher       string   `json:"publisher"`
	Version         string   `json:"version"`
	Tags            []string `json:"tags"`
	PublishedAt     string   `json:"published_at"`
	ExternalURL     string   `json:"external_url"`
	SourceRecordIDs []string `json:"source_record_ids"`
	Warnings        []string `json:"warnings"`
}

type DocumentMetadataPatch struct {
	Revision    uint64    `json:"revision"`
	Title       *string   `json:"title"`
	Publisher   *string   `json:"publisher"`
	Version     *string   `json:"version"`
	Tags        *[]string `json:"tags"`
	PublishedAt *string   `json:"published_at"`
	ExternalURL *string   `json:"external_url"`
}

const maxDocumentTags = 8
const maxDocumentTagBytes = 64

func normalizeDocumentTags(tags []string) ([]string, error) {
	if len(tags) > maxDocumentTags {
		return nil, fmt.Errorf("%w: use at most %d document tags", ErrInvalid, maxDocumentTags)
	}
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if !utf8.ValidString(tag) || strings.ContainsAny(tag, "\x00\r\n") {
			return nil, fmt.Errorf("%w: tags must use valid single-line text", ErrInvalid)
		}
		tag = strings.ToLower(strings.Join(strings.Fields(tag), " "))
		if tag == "" || len(tag) > maxDocumentTagBytes {
			return nil, fmt.Errorf("%w: tags must contain 1–%d UTF-8 bytes", ErrInvalid, maxDocumentTagBytes)
		}
		if !seen[tag] {
			normalized = append(normalized, tag)
			seen[tag] = true
		}
	}
	return normalized, nil
}

func documentMetadataDate(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil || date.Year() < 1 {
		return nil, fmt.Errorf("%w: use a valid publication date", ErrInvalid)
	}
	return &date, nil
}

func validateDocumentMetadataValue(field, value string) error {
	limit := 512
	if field == "external_url" {
		limit = 4096
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return ErrInvalid
	}
	switch field {
	case "external_url":
		if value != "" {
			parsed, err := url.ParseRequestURI(value)
			if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				return ErrInvalid
			}
		}
	case "published_at":
		_, err := documentMetadataDate(value)
		return err
	}
	return nil
}

func (s *DocumentService) SaveExtractedMetadata(ctx context.Context, id string, e DocumentExecution, metadata ExtractedDocumentMetadata) (DocumentDetails, error) {
	tags, err := normalizeDocumentTags(metadata.Tags)
	if err != nil {
		return DocumentDetails{}, err
	}
	metadata.Tags = tags
	values := map[string]string{"title": metadata.Title, "publisher": metadata.Publisher, "version": metadata.Version, "published_at": metadata.PublishedAt, "external_url": metadata.ExternalURL}
	for field, value := range values {
		if err := validateDocumentMetadataValue(field, value); err != nil {
			return DocumentDetails{}, err
		}
	}
	if len(metadata.SourceRecordIDs) > 20 || len(metadata.Warnings) > 10 {
		return DocumentDetails{}, ErrInvalid
	}
	for _, warning := range metadata.Warnings {
		if len(warning) > 1000 || !utf8.ValidString(warning) || strings.ContainsRune(warning, 0) {
			return DocumentDetails{}, ErrInvalid
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		doc, err := s.lockedExecution(ctx, tx, id, e)
		if err != nil {
			return err
		}
		if len(doc.ExtractedMetadata) > 0 {
			return nil
		}
		var total, remaining int64
		if err := tx.Model(&model.DocumentChunk{}).Where("document_id = ?", id).Count(&total).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.DocumentChunk{}).Where("document_id = ? AND completed_at IS NULL", id).Count(&remaining).Error; err != nil {
			return err
		}
		if total == 0 || remaining > 0 {
			return ErrConflict
		}
		var pages []model.DocumentPage
		if err := tx.Where("document_id = ?", id).Find(&pages).Error; err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, page := range pages {
			allowed[page.SourceRecordID] = true
		}
		seen := map[string]bool{}
		for _, sourceID := range metadata.SourceRecordIDs {
			if !allowed[sourceID] || seen[sourceID] {
				return ErrInvalid
			}
			seen[sourceID] = true
		}
		if len(seen) == 0 {
			if len(tags) > 0 {
				return fmt.Errorf("%w: extracted tags must cite original evidence", ErrInvalid)
			}
			for _, value := range values {
				if value != "" {
					return fmt.Errorf("%w: extracted metadata must cite original evidence", ErrInvalid)
				}
			}
		}
		overrides := map[string]bool{}
		if len(doc.MetadataOverrides) > 0 {
			if err := json.Unmarshal(doc.MetadataOverrides, &overrides); err != nil {
				return err
			}
		}
		data, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		updates := map[string]any{"extracted_metadata": datatypes.JSON(data), "metadata_revision": doc.MetadataRevision + 1}
		if !overrides["tags"] {
			updates["tags"] = datatypes.NewJSONSlice(tags)
		}
		for field, value := range values {
			if overrides[field] {
				continue
			}
			if field == "published_at" {
				date, _ := documentMetadataDate(value)
				updates[field] = date
			} else if field != "title" || value != "" {
				updates[field] = value
			}
		}
		return tx.Model(&doc).Updates(updates).Error
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}

func (s *DocumentService) UpdateMetadata(ctx context.Context, id, owner string, patch DocumentMetadataPatch) (DocumentDetails, error) {
	values := map[string]*string{"title": patch.Title, "publisher": patch.Publisher, "version": patch.Version, "published_at": patch.PublishedAt, "external_url": patch.ExternalURL}
	updates := map[string]any{}
	for field, value := range values {
		if value == nil {
			continue
		}
		normalized := strings.TrimSpace(*value)
		if err := validateDocumentMetadataValue(field, normalized); err != nil {
			return DocumentDetails{}, err
		}
		if field == "title" && normalized == "" {
			return DocumentDetails{}, ErrInvalid
		}
		if field == "published_at" {
			date, _ := documentMetadataDate(normalized)
			updates[field] = date
		} else {
			updates[field] = normalized
		}
	}
	if patch.Tags != nil {
		tags, err := normalizeDocumentTags(*patch.Tags)
		if err != nil {
			return DocumentDetails{}, err
		}
		updates["tags"] = datatypes.NewJSONSlice(tags)
	}
	if len(updates) == 0 {
		return DocumentDetails{}, ErrInvalid
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var doc model.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&doc, "id = ?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		if doc.UploadedBy != owner {
			return ErrForbidden
		}
		if documentIsProcessing(doc.Status) || doc.Status == "uploading" || doc.MetadataRevision != patch.Revision {
			return ErrConflict
		}
		overrides := map[string]bool{}
		if len(doc.MetadataOverrides) > 0 {
			if err := json.Unmarshal(doc.MetadataOverrides, &overrides); err != nil {
				return err
			}
		}
		for field := range updates {
			overrides[field] = true
		}
		data, err := json.Marshal(overrides)
		if err != nil {
			return err
		}
		updates["metadata_overrides"] = datatypes.JSON(data)
		updates["metadata_revision"] = doc.MetadataRevision + 1
		return tx.Model(&doc).Updates(updates).Error
	})
	if err != nil {
		return DocumentDetails{}, err
	}
	return s.Get(ctx, id)
}
