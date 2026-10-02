package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

func TestDocumentIndexingFailureResumesWithoutRepeatingExtraction(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	extractor := &fixtureExtractor{pages: []string{"Brake requirements and qualifications."}}
	s := NewDocumentService(db, t.TempDir(), extractor)
	details, err := s.Import(ctx, bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	doc := details.Document
	chunks := buildDocumentChunks(doc.ID, details.Pages, extractor.pages)
	if err := db.Create(&chunks).Error; err != nil {
		t.Fatal(err)
	}
	doc.Generation = 1
	for _, name := range []string{"uploading", "extracting"} {
		setDocumentStage(&doc, name, "running")
		setDocumentStage(&doc, name, "completed")
	}
	setDocumentStage(&doc, "indexing", "running")
	taskID, sessionID := identifier.New(identifier.Prefix("task")), identifier.New(identifier.Session)
	conversationID := identifier.New(identifier.Conversation)
	if err := saveDocumentProgress(db, &doc, map[string]any{"status": "indexing", "review_status": model.DocumentReviewPending, "generation": doc.Generation, "task_id": taskID}); err != nil {
		t.Fatal(err)
	}
	recordID := details.Pages[0].SourceRecordID
	if err := db.Where("source_record_id = ?", recordID).Delete(&model.SourceChunk{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("source_record_id = ?", recordID).Delete(&model.SourceRecordIndex{}).Error; err != nil {
		t.Fatal(err)
	}
	var generation atomic.Uint64
	generation.Store(1)
	var runtimeStatus atomic.Value
	runtimeStatus.Store("running")
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/document-attempts/validate" {
			json.NewEncoder(w).Encode(map[string]any{"task_id": taskID, "session_id": sessionID, "conversation_id": conversationID, "attempt_number": 1, "input": map[string]any{"document_id": doc.ID, "generation": generation.Load()}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/v1/tasks/") {
			json.NewEncoder(w).Encode(map[string]any{"status": runtimeStatus.Load(), "error": map[string]string{"message": "Search indexing unavailable"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer runtime.Close()
	s.SetIntegration(nil, NewDocumentRuntimeClient(runtime.URL, "internal"))
	indexFailure := errors.New("search index unavailable")
	if err := db.Callback().Create().Before("gorm:create").Register("test:index_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "knowledge_source_record_index" {
			tx.AddError(indexFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove("test:index_failure") })
	execution := DocumentExecution{Generation: 1}
	if _, err := s.Prepare(ctx, doc.ID, execution); !errors.Is(err, indexFailure) {
		t.Fatalf("indexing failure: %v", err)
	}
	runtimeStatus.Store("failed")
	if err := s.dispatchDocuments(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := s.Get(ctx, doc.ID)
	if err != nil || failed.Document.Status != "failed" || failed.Document.Stages[2].Status != "failed" || !documentStageComplete(failed.Document, "extracting") || len(failed.Pages) != 1 {
		t.Fatalf("failure lost extraction or the failing stage: %+v %v", failed.Document, err)
	}
	if err := db.Callback().Create().Remove("test:index_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Control(ctx, doc.ID, doc.UploadedBy, "retry"); err != nil {
		t.Fatal(err)
	}
	generation.Store(2)
	execution.Generation = 2
	resumed, err := s.Prepare(ctx, doc.ID, execution)
	if err != nil || resumed.Document.Status != "reading" || !documentStageComplete(resumed.Document, "indexing") || extractor.calls != 1 {
		t.Fatalf("retry did not reuse extracted evidence: %+v %v", resumed.Document, err)
	}
	if !resumed.Document.Stages[1].CompletedAt.Equal(*failed.Document.Stages[1].CompletedAt) || resumed.Document.Stages[2].Generation != 2 {
		t.Fatal("retry rewrote completed extraction or retained the old indexing generation")
	}
	result, err := s.SearchSources(ctx, DocumentSearchOptions{Query: "brake", DocumentID: doc.ID, Limit: 8}, DocumentRetrievalScope{Team: true})
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("retry did not restore search: %+v %v", result, err)
	}
}

func TestStoppingConcurrentDocumentStagesPreservesCompletedWork(t *testing.T) {
	doc := model.Document{Generation: 1}
	for _, name := range []string{"uploading", "extracting", "indexing"} {
		setDocumentStage(&doc, name, "running")
		setDocumentStage(&doc, name, "completed")
	}
	setDocumentStage(&doc, "reading", "running")
	setDocumentStage(&doc, "identifying_metadata", "running")
	stopDocumentStage(&doc, "cancelled")
	if doc.Stages[3].Status != "cancelled" || doc.Stages[4].Status != "cancelled" || !documentStageComplete(doc, "indexing") || doc.Stages[5].Status != "waiting" {
		t.Fatalf("concurrent stages were not stopped correctly: %+v", doc.Stages)
	}
	doc.Generation++
	resetUnfinishedDocumentStages(&doc)
	if doc.Stages[3].Status != "waiting" || doc.Stages[4].Status != "waiting" || !documentStageComplete(doc, "indexing") {
		t.Fatalf("retry lost completed work: %+v", doc.Stages)
	}
}
