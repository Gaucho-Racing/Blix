package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

type fixtureExtractor struct {
	pages []string
	err   error
	calls int
}

func (e *fixtureExtractor) Extract(context.Context, string) ([]string, error) {
	e.calls++
	return e.pages, e.err
}
func documentMetadata() DocumentMetadata {
	return DocumentMetadata{Title: "Test official rules", Filename: "rules.pdf", Publisher: "Test publisher", Version: "1.0", Tags: []string{"formula sae", "braking"}, PublishedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UploadedBy: identifier.New(identifier.Entity)}
}
func TestDocumentImportPreservesEvidenceAndDeduplicates(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	directory := t.TempDir()
	extractor := &fixtureExtractor{pages: []string{"First page: the brake rules require two hydraulic circuits.", "Second page: testing requirements.", ""}}
	service := NewDocumentService(db, directory, extractor)
	metadata := documentMetadata()
	data := []byte("%PDF-1.7\n" + identifier.New(identifier.Document))
	result, err := service.Import(context.Background(), bytes.NewReader(data), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.PageCount != 3 || len(result.Pages) != 3 || result.Pages[2].CharacterCount != 0 {
		t.Fatalf("pages=%+v", result)
	}
	stored, err := os.ReadFile(filepath.Join(directory, result.Document.SHA256+".pdf"))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("original not retained: %v", err)
	}
	for index, page := range result.Pages {
		record, err := service.Page(context.Background(), result.Document.ID, page.Number)
		if err != nil {
			t.Fatal(err)
		}
		if record.Content != extractor.pages[index] || record.EvidenceType != "quoted_source" || record.CaptureMethod != "document_upload" || !record.OccurredAt.Equal(metadata.PublishedAt) {
			t.Fatalf("record=%+v", record)
		}
		var provenance map[string]any
		if err := json.Unmarshal(record.Metadata, &provenance); err != nil {
			t.Fatal(err)
		}
		if provenance["document_id"] != result.Document.ID || provenance["page_number"] != float64(index+1) || provenance["version"] != "1.0" {
			t.Fatalf("provenance=%+v", provenance)
		}
	}
	citations, err := NewWikiService(db, nil).resolveCitations(context.Background(), []string{result.Pages[0].SourceRecordID})
	if err != nil || citations[result.Pages[0].SourceRecordID].DocumentID != result.Document.ID || citations[result.Pages[0].SourceRecordID].PDFPage != 1 {
		t.Fatalf("document citations=%+v err=%v", citations, err)
	}
	duplicate, err := service.Import(context.Background(), bytes.NewReader(data), metadata)
	if err != nil || duplicate.Document.ID != result.Document.ID || extractor.calls != 1 {
		t.Fatalf("duplicate=%+v err=%v calls=%d", duplicate, err, extractor.calls)
	}
	metadata.Version = "2.0"
	if _, err := service.Import(context.Background(), bytes.NewReader(data), metadata); !errors.Is(err, ErrConflict) {
		t.Fatalf("metadata conflict=%v", err)
	}
	if _, err := service.Page(context.Background(), result.Document.ID, 4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing page=%v", err)
	}
}
func TestDocumentRejectsInvalidInputAndExtractionFailure(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	extractor := &fixtureExtractor{err: errors.Join(ErrInvalid, errors.New("invalid PDF"))}
	service := NewDocumentService(db, t.TempDir(), extractor)
	metadata := documentMetadata()
	for _, data := range [][]byte{[]byte("not a PDF"), []byte("%PDF-" + strings.Repeat("x", MaxDocumentBytes))} {
		if _, err := service.Import(context.Background(), bytes.NewReader(data), metadata); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input accepted: %v", err)
		}
	}
	invalid := metadata
	invalid.ExternalURL = "javascript:alert(1)"
	if _, err := service.Import(context.Background(), strings.NewReader("%PDF-test"), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	invalid = metadata
	invalid.PublishedAt = time.Time{}
	if _, err := service.Import(context.Background(), strings.NewReader("%PDF-test"), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := service.Import(context.Background(), strings.NewReader("%PDF-test"), metadata); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.Document{}).Where("uploaded_by = ?", metadata.UploadedBy).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed import left documents: %d %v", count, err)
	}
	files, err := os.ReadDir(service.directory)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed import left files: %v %v", files, err)
	}
}
func TestPopplerExtractDocument(t *testing.T) {
	path := os.Getenv("BLIX_TEST_PDF_PATH")
	if path == "" {
		t.Skip("BLIX_TEST_PDF_PATH is required for real PDF acceptance tests")
	}
	pages, err := (PopplerExtractor{}).Extract(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 1 || len(pages[0]) < 20 {
		t.Fatal("missing page text")
	}
	db, _, _, _ := indexFixture(t)
	service := NewDocumentService(db, t.TempDir(), PopplerExtractor{})
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	metadata := documentMetadata()
	metadata.Filename = filepath.Base(path)
	result, err := service.Import(context.Background(), file, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.PageCount != len(pages) {
		t.Fatalf("expected %d pages, got %d", len(pages), result.Document.PageCount)
	}
	t.Logf("extracted and retained %d pages", len(pages))
}

type blockingPDFExtractor struct {
	started chan struct{}
	release chan struct{}
}

func (e blockingPDFExtractor) Extract(context.Context, string) ([]string, error) {
	e.started <- struct{}{}
	<-e.release
	return []string{"Brake system requirements from the original PDF."}, nil
}
func TestConcurrentDocumentImportIsIdempotentAndBounded(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	extractor := blockingPDFExtractor{started: make(chan struct{}, 2), release: make(chan struct{})}
	service := NewDocumentService(db, t.TempDir(), extractor)
	metadata := documentMetadata()
	data := "%PDF-test-" + identifier.New(identifier.Document)
	type outcome struct {
		details DocumentDetails
		err     error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			details, err := service.Import(context.Background(), strings.NewReader(data), metadata)
			results <- outcome{details, err}
		}()
	}
	for range 2 {
		select {
		case <-extractor.started:
		case <-time.After(5 * time.Second):
			close(extractor.release)
			t.Fatal("imports did not reach extraction")
		}
	}
	_, busy := service.Import(context.Background(), strings.NewReader(data), metadata)
	close(extractor.release)
	if !errors.Is(busy, ErrUnavailable) {
		t.Fatalf("unbounded extraction: %v", busy)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.details.Document.ID != second.details.Document.ID {
		t.Fatalf("concurrent imports diverged: %+v %+v", first, second)
	}
	original, err := os.ReadFile(filepath.Join(service.directory, first.details.Document.SHA256+".pdf"))
	if err != nil || string(original) != data {
		t.Fatalf("lost original: %v", err)
	}
}
