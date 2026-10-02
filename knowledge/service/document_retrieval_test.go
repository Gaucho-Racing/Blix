package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestSourcePassagesPreserveUnicodeEvidence(t *testing.T) {
	text := "  # Requirements\n\n" + strings.Repeat("Brake pressure — μ = 0.8; exceptions apply.\n", 180) + "\n\nT.3.2 Final requirement."
	record := model.SourceRecord{ID: identifier.New(identifier.SourceRecord), Content: text}
	chunks := sourceChunks(record)
	if len(chunks) < 2 {
		t.Fatal("large record was not split")
	}
	covered := make([]bool, len(text))
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk.Content) || len(chunk.Content) > sourcePassageBytes || chunk.Content != text[chunk.StartOffset:chunk.EndOffset] || chunk.Ordinal != i+1 {
			t.Fatalf("invalid evidence span: %+v", chunk)
		}
		for offset := chunk.StartOffset; offset < chunk.EndOffset; offset++ {
			covered[offset] = true
		}
	}
	for offset, r := range text {
		if !unicode.IsSpace(r) && !covered[offset] {
			t.Fatalf("lost evidence at offset %d", offset)
		}
	}
	if chunks[len(chunks)-1].RuleIDs[0] != "T.3.2" {
		t.Fatal("rule identifier was not retained")
	}
}

func TestDocumentScopedRetrievalBeforeIngestionAndAfterRejection(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	importDoc := func(pages []string) DocumentDetails {
		t.Helper()
		s := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: pages})
		doc, err := s.Import(ctx, bytes.NewBufferString("%PDF-test-"+identifier.New(identifier.Document)), documentMetadata())
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := importDoc([]string{"T.3.2 Brake pressure requires two circuits.\nExceptions: inspect the next page.", "Testing qualifications and participation in 2027.", ""})
	second := importDoc([]string{"T.3.20 Brake pressure is a different rule.", "Unrelated battery voltage."})
	if err := db.Model(&first.Document).Updates(map[string]any{"review_status": model.DocumentReviewPending, "status": "reading"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewDocumentService(db, t.TempDir(), nil)
	scope := DocumentRetrievalScope{DocumentIDs: []string{first.Document.ID}}
	search := func(query string, scope DocumentRetrievalScope, id string) DocumentSearchResult {
		t.Helper()
		result, err := s.SearchSources(ctx, DocumentSearchOptions{Query: query, Limit: 8, DocumentID: id}, scope)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := search("brake pressure", scope, "")
	if len(result.Results) != 1 || result.Results[0].DocumentID != first.Document.ID || result.Results[0].PDFPage != 1 || result.Results[0].SourceRecordID != first.Pages[0].SourceRecordID {
		t.Fatalf("wrong scoped evidence: %+v", result)
	}
	if !result.Documents[0].ReadyForQuestions || result.Documents[0].ReviewStatus != model.DocumentReviewPending {
		t.Fatal("reading must not delay source search or implicitly approve a document")
	}
	if !strings.Contains(result.Results[0].CitationURL, "preview=1&page=1") {
		t.Fatal("missing page citation")
	}
	if len(search("brake pressure", DocumentRetrievalScope{}, "").Results) != 0 {
		t.Fatal("empty scope leaked pending evidence")
	}
	if len(search("brake pressure", DocumentRetrievalScope{Team: true}, first.Document.ID).Results) != 0 {
		t.Fatal("pending evidence leaked into team search")
	}
	if _, err := s.SearchSources(ctx, DocumentSearchOptions{Query: "brakes", Limit: 8, DocumentID: second.Document.ID}, scope); !errors.Is(err, ErrForbidden) {
		t.Fatalf("scope override accepted: %v", err)
	}
	both := DocumentRetrievalScope{DocumentIDs: []string{first.Document.ID, second.Document.ID}}
	exact := search("T.3.2", both, "")
	if len(exact.Results) != 1 || exact.Results[0].DocumentID != first.Document.ID {
		t.Fatalf("rule identifiers matched by prefix: %+v", exact)
	}
	for _, query := range []string{"unobtainium", "the and is", "' & ! | :*"} {
		if len(search(query, scope, "").Results) != 0 {
			t.Fatalf("invalid no-answer behavior for %q", query)
		}
	}
	page, err := s.ReadEvidencePage(ctx, first.Document.ID, 2, 0, scope)
	if err != nil || page.SourceRecordID != first.Pages[1].SourceRecordID || !strings.Contains(page.Content, "2027") {
		t.Fatalf("adjacent page read: %+v %v", page, err)
	}
	if err := db.Model(&first.Document).Update("review_status", model.DocumentReviewIngested).Error; err != nil {
		t.Fatal(err)
	}
	if len(search("brake pressure", DocumentRetrievalScope{Team: true}, first.Document.ID).Results) != 1 {
		t.Fatal("approved document did not become team-searchable")
	}
	if err := db.Model(&first.Document).Update("review_status", model.DocumentReviewRejected).Error; err != nil {
		t.Fatal(err)
	}
	if len(search("brake pressure", scope, "").Results) != 0 {
		t.Fatal("rejected document remained searchable")
	}
	if _, err := s.ReadEvidencePage(ctx, first.Document.ID, 1, 0, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected page remained agent-readable: %v", err)
	}
	if _, err := s.Page(ctx, first.Document.ID, 1); err != nil {
		t.Fatalf("human audit evidence was lost: %v", err)
	}
}

func TestDocumentEvidenceReadIsBoundedAndIndexCanRebuild(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	text := strings.Repeat("Requirements — αβγ. ", 1200)
	s := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: []string{text}})
	doc, err := s.Import(ctx, bytes.NewBufferString("%PDF-test-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	scope := DocumentRetrievalScope{DocumentIDs: []string{doc.Document.ID}}
	var reconstructed strings.Builder
	cursor := 0
	for {
		page, err := s.ReadEvidencePage(ctx, doc.Document.ID, 1, cursor, scope)
		if err != nil || len(page.Content) > 8000 || !utf8.ValidString(page.Content) {
			t.Fatalf("invalid bounded page read: %+v %v", page, err)
		}
		reconstructed.WriteString(page.Content)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if reconstructed.String() != text {
		t.Fatal("paginated read lost source text")
	}
	if err := db.Where("source_record_id = ?", doc.Pages[0].SourceRecordID).Delete(&model.SourceChunk{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("source_record_id = ?", doc.Pages[0].SourceRecordID).Delete(&model.SourceRecordIndex{}).Error; err != nil {
		t.Fatal(err)
	}
	sources := NewSourceService(db)
	for i := 0; i < 20; i++ {
		count, err := sources.IndexPendingRecords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if count < 50 {
			break
		}
	}
	result, err := s.SearchSources(ctx, DocumentSearchOptions{Query: "requirements", Limit: 20}, scope)
	if err != nil || len(result.Results) == 0 {
		t.Fatalf("rebuild did not restore retrieval: %v", err)
	}
	budget := 0
	for _, hit := range result.Results {
		budget += len(hit.Content)
	}
	if budget > 16000 {
		t.Fatal("retrieval exceeded the context budget")
	}
	var before, after int64
	db.Model(&model.SourceChunk{}).Where("source_record_id = ?", doc.Pages[0].SourceRecordID).Count(&before)
	if _, err := sources.IndexPendingRecords(ctx); err != nil {
		t.Fatal(err)
	}
	db.Model(&model.SourceChunk{}).Where("source_record_id = ?", doc.Pages[0].SourceRecordID).Count(&after)
	if before != after {
		t.Fatal("index retry duplicated chunks")
	}
}
