package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestDocumentIndexProgressAndProvenance(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	docs := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: []string{"T.3.2 braking hydraulic pressure", "T.3.2 braking hydraulic pressure", "second evidence unrelated"}})
	imported, err := docs.Import(ctx, bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&imported.Document).Update("status", "ready")
	get := func() DocumentDetails {
		t.Helper()
		d, err := docs.Get(ctx, imported.Document.ID)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := get(); d.SearchIndex.Status != "disabled" || d.SearchIndex.Total != 3 || d.SearchIndex.IndexedRecords != 3 {
		t.Fatalf("disabled: %+v", d.SearchIndex)
	}
	namespace := identifier.New(identifier.Session)
	provider := &fixtureEmbeddings{fail: true}
	embeddings := NewEmbeddingService(db, provider, namespace, 2)
	docs.SetEmbeddings(embeddings)
	if d := get(); d.SearchIndex.Status != "running" || d.SearchIndex.Pending != 3 {
		t.Fatalf("not yet seeded: %+v", d.SearchIndex)
	}
	if err := embeddings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := embeddings.ProcessBatch(ctx); err == nil {
		t.Fatal("expected provider failure")
	}
	if d := get(); d.SearchIndex.Status != "retry" || d.SearchIndex.Retrying != 3 {
		t.Fatalf("retry: %+v", d.SearchIndex)
	}
	provider.fail = false
	db.Model(&model.Embedding{}).Where("namespace=?", namespace).Update("available_at", time.Now().Add(-time.Minute))
	if _, err := embeddings.ProcessBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if d := get(); d.SearchIndex.Status != "ready" || d.SearchIndex.Ready != 3 || d.SearchIndex.Pending != 0 || d.SearchIndex.Namespace != namespace || d.SearchIndex.Dimensions != 2 {
		t.Fatalf("ready chunk links including dedup: %+v", d.SearchIndex)
	}
	page, err := docs.IndexDetails(ctx, imported.Document.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) != 1 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("pagination: %+v", page)
	}
	c := page.Chunks[0]
	if c.SourceRecordID != imported.Pages[0].SourceRecordID || c.PDFPage != 1 || c.Kind != "text" || c.EmbeddingStatus != "ready" || c.EndOffset != len(c.Content) || len(c.RuleIDs) != 2 || c.IndexedAt == nil {
		t.Fatalf("provenance: %+v", c)
	}
	encoded, err := json.Marshal(page)
	if err != nil || strings.Contains(string(encoded), "\"vector\"") || strings.Contains(string(encoded), "search_vector") {
		t.Fatalf("raw vector exposed: %s %v", encoded, err)
	}
	if _, err := docs.IndexDetails(ctx, imported.Document.ID, -1, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid offset: %v", err)
	}
	if _, err := docs.IndexDetails(ctx, identifier.New(identifier.Document), 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing document: %v", err)
	}
	docs.SetEmbeddings(NewEmbeddingService(db, provider, "new-"+namespace, 2))
	if d := get(); d.SearchIndex.Ready != 0 || d.SearchIndex.Pending != 3 {
		t.Fatalf("stale model counted: %+v", d.SearchIndex)
	}
	docs.SetEmbeddings(embeddings)
	visual, err := NewSourceService(db).IngestSourceRecord(ctx, imported.Document.SourceID, IngestSourceRecordInput{ExternalID: "visual:test", RecordType: "visual_description", Content: "Diagram shows hydraulic braking circuits", EvidenceType: "agent_summary", CaptureMethod: "document_vision", OccurredAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&model.DocumentPage{}).Where("document_id=? AND number=1", imported.Document.ID).Update("visual_source_record_id", visual.Record.ID)
	if d := get(); d.SearchIndex.Status != "running" || d.SearchIndex.Total != 4 || d.SearchIndex.VisualChunks != 1 || d.SearchIndex.SourceRecords != 4 {
		t.Fatalf("visual arrived: %+v", d.SearchIndex)
	}
	all, err := docs.IndexDetails(ctx, imported.Document.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Chunks) != 4 || all.NextOffset != nil || all.Chunks[1].Kind != "visual" || all.Chunks[1].SourceRecordID != visual.Record.ID {
		t.Fatalf("visual provenance: %+v", all)
	}
	db.Model(&imported.Document).Update("review_status", model.DocumentReviewRejected)
	if d := get(); d.SearchIndex.Status != "excluded" {
		t.Fatalf("rejected: %+v", d.SearchIndex)
	}
}
