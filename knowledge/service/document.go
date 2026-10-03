package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/depot"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const MaxDocumentBytes = 20 * 1024 * 1024

type DocumentMetadata struct {
	Title       string
	Filename    string
	Publisher   string
	Version     string
	Tags        []string
	ExternalURL string
	PublishedAt time.Time
	UploadedBy  string
}

type DocumentDetails struct {
	SearchIndex       DocumentSearchIndex          `json:"search_index"`
	ReadyForQuestions bool                         `json:"ready_for_questions"`
	Document          model.Document               `json:"document"`
	Pages             []model.DocumentPage         `json:"pages"`
	Chunks            []model.DocumentChunk        `json:"chunks"`
	Notes             *model.DocumentNotesRevision `json:"notes"`
	Changesets        []model.Changeset            `json:"changesets"`
	ReviewBlocked     bool                         `json:"review_blocked"`
}

type PDFExtractor interface {
	Extract(context.Context, string) ([]string, error)
}
type DocumentService struct {
	db         *gorm.DB
	embeddings *EmbeddingService
	directory  string
	extractor  PDFExtractor
	slots      chan struct{}
	depot      *depot.Client
	runtime    *DocumentRuntimeClient
}

func NewDocumentService(db *gorm.DB, directory string, extractor PDFExtractor) *DocumentService {
	return &DocumentService{db: db, directory: directory, extractor: extractor, slots: make(chan struct{}, 2)}
}

func (s *DocumentService) Import(ctx context.Context, reader io.Reader, metadata DocumentMetadata) (DocumentDetails, error) {
	if s.extractor == nil {
		return DocumentDetails{}, ErrUnavailable
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return DocumentDetails{}, ctx.Err()
	default:
		return DocumentDetails{}, fmt.Errorf("%w: document imports are busy; try again shortly", ErrUnavailable)
	}

	for _, value := range []string{metadata.Title, metadata.Publisher, metadata.Version} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return DocumentDetails{}, ErrInvalid
		}
	}
	tags, err := normalizeDocumentTags(metadata.Tags)
	if err != nil {
		return DocumentDetails{}, err
	}
	metadata.Tags = tags
	if metadata.PublishedAt.IsZero() || !identifier.IsValid(metadata.UploadedBy, identifier.Entity) {
		return DocumentDetails{}, ErrInvalid
	}
	if metadata.ExternalURL != "" {
		parsed, err := url.ParseRequestURI(metadata.ExternalURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || len(metadata.ExternalURL) > 4096 {
			return DocumentDetails{}, fmt.Errorf("%w: source URL must be HTTP or HTTPS without credentials", ErrInvalid)
		}
	}
	if strings.ContainsAny(metadata.Filename, "\x00\r\n") || len(metadata.Filename) > 512 {
		return DocumentDetails{}, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxDocumentBytes+1))
	if err != nil {
		return DocumentDetails{}, err
	}
	if len(data) > MaxDocumentBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return DocumentDetails{}, fmt.Errorf("%w: upload a PDF up to 20 MiB", ErrInvalid)
	}
	hash := hashBytes(data)
	var existing model.Document
	err = s.db.WithContext(ctx).First(&existing, "sha256 = ?", hash).Error
	if err == nil {
		if !documentMetadataMatches(existing, metadata) {
			return DocumentDetails{}, fmt.Errorf("%w: this PDF is already imported with different metadata", ErrConflict)
		}
		return s.Get(ctx, existing.ID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return DocumentDetails{}, err
	}
	if err := os.MkdirAll(s.directory, 0750); err != nil {
		return DocumentDetails{}, fmt.Errorf("create document storage: %w", err)
	}
	temp, err := os.CreateTemp(s.directory, "upload-*.pdf")
	if err != nil {
		return DocumentDetails{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return DocumentDetails{}, err
	}
	if err := temp.Close(); err != nil {
		return DocumentDetails{}, err
	}
	pages, err := s.extractor.Extract(ctx, tempPath)
	if err != nil {
		return DocumentDetails{}, err
	}
	if len(pages) == 0 || len(pages) > 500 {
		return DocumentDetails{}, ErrInvalid
	}
	if err := os.Chmod(tempPath, 0640); err != nil {
		return DocumentDetails{}, err
	}
	if err := os.Rename(tempPath, filepath.Join(s.directory, hash+".pdf")); err != nil {
		return DocumentDetails{}, err
	}
	result := DocumentDetails{Pages: []model.DocumentPage{}}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", hash).Error; err != nil {
			return err
		}
		var existing model.Document
		if err := tx.First(&existing, "sha256 = ?", hash).Error; err == nil {
			if !documentMetadataMatches(existing, metadata) {
				return fmt.Errorf("%w: this PDF is already imported with different metadata", ErrConflict)
			}
			var err error
			result, err = NewDocumentService(tx, s.directory, s.extractor).Get(ctx, existing.ID)
			return err
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		id := identifier.New(identifier.Document)
		sourceMetadata, _ := json.Marshal(map[string]any{"document_id": id, "sha256": hash, "publisher": metadata.Publisher, "version": metadata.Version, "tags": metadata.Tags})
		sources := NewSourceService(tx)
		source, err := sources.CreateSource(ctx, CreateSourceInput{Kind: "pdf_document", ExternalID: hash, Name: metadata.Title, Metadata: sourceMetadata})
		if err != nil {
			return err
		}
		document := model.Document{ID: id, ReviewStatus: model.DocumentReviewInReview, SHA256: hash, SourceID: source.ID, Title: metadata.Title, Filename: filepath.Base(metadata.Filename), Publisher: metadata.Publisher, Version: metadata.Version, Tags: datatypes.NewJSONSlice(metadata.Tags), ExternalURL: metadata.ExternalURL, PublishedAt: &metadata.PublishedAt, UploadedBy: metadata.UploadedBy, PageCount: len(pages), SizeBytes: int64(len(data))}
		if err := tx.Create(&document).Error; err != nil {
			return err
		}
		for index, content := range pages {
			pageMetadata, _ := json.Marshal(map[string]any{"document_id": id, "page_number": index + 1, "document_tags": metadata.Tags, "publisher": metadata.Publisher, "version": metadata.Version, "sha256": hash})
			record, err := sources.IngestSourceRecord(ctx, source.ID, IngestSourceRecordInput{ExternalID: "page:" + strconv.Itoa(index+1), ExternalVersion: hash, RecordType: "pdf_page", Content: content, Metadata: pageMetadata, ExternalURL: metadata.ExternalURL, OccurredAt: metadata.PublishedAt, EvidenceType: "quoted_source", CaptureMethod: "document_upload"})
			if err != nil {
				return err
			}
			page := model.DocumentPage{DocumentID: id, Number: index + 1, SourceRecordID: record.Record.ID, CharacterCount: utf8.RuneCountInString(content)}
			if err := tx.Create(&page).Error; err != nil {
				return err
			}
			result.Pages = append(result.Pages, page)
		}
		result.Document = document
		return nil
	})
	return result, err
}
func (s *DocumentService) Get(ctx context.Context, id string) (DocumentDetails, error) {
	if !identifier.IsValid(id, identifier.Document) {
		return DocumentDetails{}, ErrInvalid
	}
	result := DocumentDetails{Pages: []model.DocumentPage{}, Chunks: []model.DocumentChunk{}, Changesets: []model.Changeset{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&result.Document, "id = ?", id).Error; err != nil {
			return mapDatabaseError(err)
		}
		if err := tx.Where("document_id = ?", id).Order("number").Find(&result.Pages).Error; err != nil {
			return err
		}
		if err := tx.Omit("evidence", "findings").Where("document_id = ?", id).Order("number").Find(&result.Chunks).Error; err != nil {
			return err
		}
		var notes []model.DocumentNotesRevision
		if err := tx.Where("document_id = ?", id).Order("revision DESC").Limit(1).Find(&notes).Error; err != nil {
			return err
		}
		if len(notes) > 0 {
			result.Notes = &notes[0]
		}
		if err := documentChangesets(tx, result.Document).Order("created_at").Find(&result.Changesets).Error; err != nil {
			return err
		}
		for _, changeset := range result.Changesets {
			if documentPublicationStarted(changeset) {
				result.ReviewBlocked = true
				break
			}
		}
		var indexed int64
		if err := tx.Model(&model.SourceRecordIndex{}).Where("chunker_version = ? AND source_record_id IN (SELECT source_record_id FROM knowledge_document_page WHERE document_id = ?)", sourceChunkerVersion, id).Count(&indexed).Error; err != nil {
			return err
		}
		result.ReadyForQuestions = result.Document.ReviewStatus != model.DocumentReviewRejected && result.Document.PageCount > 0 && indexed == int64(result.Document.PageCount)
		var err error
		result.SearchIndex, err = s.documentSearchIndex(tx, result.Document)
		if err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
func (s *DocumentService) List(ctx context.Context) ([]model.Document, error) {
	documents := []model.Document{}
	err := s.db.WithContext(ctx).Order("created_at DESC").Limit(100).Find(&documents).Error
	return documents, err
}
func (s *DocumentService) Page(ctx context.Context, id string, number int) (SourceRecordDetail, error) {
	if !identifier.IsValid(id, identifier.Document) || number < 1 {
		return SourceRecordDetail{}, ErrInvalid
	}
	var page model.DocumentPage
	if err := s.db.WithContext(ctx).First(&page, "document_id = ? AND number = ?", id, number).Error; err != nil {
		return SourceRecordDetail{}, mapDatabaseError(err)
	}
	var record model.SourceRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ? AND source_id IN (SELECT source_id FROM knowledge_document WHERE id = ?)", page.SourceRecordID, id).Error; err != nil {
		return SourceRecordDetail{}, mapDatabaseError(err)
	}
	detail := SourceRecordDetail{SourceRecord: record}
	if page.VisualSourceRecordID != "" {
		var visual model.SourceRecord
		if err := s.db.WithContext(ctx).First(&visual, "id = ?", page.VisualSourceRecordID).Error; err != nil {
			return SourceRecordDetail{}, err
		}
		detail.VisualRecord = &visual
	}
	images, err := s.ChunkImages(ctx, id, number, number)
	if err != nil {
		return SourceRecordDetail{}, err
	}
	if len(images) == 1 {
		detail.PageImage = &images[0]
	}
	return detail, nil
}
func (s *DocumentService) File(ctx context.Context, id string) (string, string, error) {
	details, err := s.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(s.directory, details.Document.SHA256+".pdf"), details.Document.Filename, nil
}

func documentMetadataMatches(document model.Document, metadata DocumentMetadata) bool {
	return document.Title == metadata.Title && document.Publisher == metadata.Publisher && document.Version == metadata.Version && slices.Equal(document.Tags, metadata.Tags) && document.PublishedAt != nil && document.PublishedAt.Equal(metadata.PublishedAt) && document.ExternalURL == metadata.ExternalURL
}
