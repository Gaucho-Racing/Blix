package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/depot"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestAsyncDocumentLifecycleRetainsWorkAndVerifiesOriginal(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	if err := db.Exec("DROP INDEX IF EXISTS idx_knowledge_document_sha256").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []byte("%PDF-test-" + identifier.New(identifier.Document))
	fileID := "file_test"
	docID := ""
	generation := uint64(1)
	taskID := identifier.New(identifier.Prefix("task"))
	cid := identifier.New(identifier.Conversation)
	session := identifier.New(identifier.Session)
	status := "running"
	attempt := uint64(1)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer existing-sentinel-token" {
			t.Error("Depot must use the existing service token")
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/buckets/blix-documents/uploads":
			json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": fileID}, "upload_url": "https://s3.example.test/upload", "method": "PUT"})
		case strings.HasSuffix(r.URL.Path, "/complete"):
			json.NewEncoder(w).Encode(map[string]any{"id": fileID, "status": "ACTIVE", "size_bytes": len(data), "content_type": "application/pdf"})
		case r.URL.Path == "/download/"+fileID:
			w.Write(data)
		default:
			w.WriteHeader(404)
		}
	}))
	defer storage.Close()
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/internal/v1/document-ingestions":
			json.NewEncoder(w).Encode(map[string]string{"conversation_id": cid, "task_id": taskID})
		case r.URL.Path == "/internal/v1/document-attempts/validate":
			var request DocumentExecution
			json.NewDecoder(r.Body).Decode(&request)
			if request.FencingToken != "current" {
				w.WriteHeader(409)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"task_id": taskID, "session_id": session, "conversation_id": cid, "attempt_number": attempt, "input": map[string]any{"document_id": docID, "generation": generation}})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			status = "cancelled"
			json.NewEncoder(w).Encode(map[string]any{})
		case strings.HasPrefix(r.URL.Path, "/internal/v1/tasks/"):
			json.NewEncoder(w).Encode(map[string]string{"status": status})
		default:
			w.WriteHeader(404)
		}
	}))
	defer runtime.Close()
	extractor := &fixtureExtractor{pages: []string{"# Brakes\n" + strings.Repeat("Requirements and exceptions. ", 1000), "Brake diagram without readable text.", ""}}
	service := NewDocumentService(db, t.TempDir(), extractor)
	service.SetIntegration(depot.NewClient(storage.URL, "blix-documents", "existing-sentinel-token"), NewDocumentRuntimeClient(runtime.URL, "internal"))
	metadata := documentMetadata()
	caller := CallerContext{EntityID: metadata.UploadedBy, EntityType: "USER", UserID: identifier.New(identifier.User)}
	intent, err := service.InitiateUpload(ctx, metadata, caller, "Preserve braking rules and exceptions.")
	if err != nil {
		t.Fatal(err)
	}
	docID = intent.Document.ID
	if extractor.calls != 0 || intent.Document.Status != "uploading" {
		t.Fatal("upload initiation performed extraction")
	}
	if _, err := service.CompleteUpload(ctx, docID, identifier.New(identifier.Entity)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign completion: %v", err)
	}
	completed, err := service.CompleteUpload(ctx, docID, caller.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Document.Status != "queued" || extractor.calls != 0 {
		t.Fatal("completion must queue without extraction")
	}
	if _, err := service.CompleteUpload(ctx, docID, caller.EntityID); err != nil {
		t.Fatal(err)
	}
	if err := service.dispatchDocuments(ctx); err != nil {
		t.Fatal(err)
	}
	execution := DocumentExecution{AttemptID: "attempt_test", WorkerID: "worker_test", FencingToken: "current", Generation: 1}
	prepared, err := service.Prepare(ctx, docID, execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Chunks) < 2 || len(prepared.Pages) != 3 || extractor.calls != 1 {
		t.Fatalf("manifest: %+v", prepared)
	}
	first, err := service.ReadChunk(ctx, docID, 1)
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{}
	for _, page := range first["evidence"].([]DocumentEvidence) {
		sources = append(sources, page.SourceRecordID)
	}
	save := SaveDocumentChunk{DocumentExecution: execution, Revision: 0, Findings: "Verified requirement " + strings.Join(sources, " "), Notes: "Notebook with exact requirements " + sources[0], SourceRecordIDs: sources}
	saved, err := service.SaveChunk(ctx, docID, 1, save)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Notes.Revision != 1 {
		t.Fatal("notes were not persisted")
	}
	if _, err := service.SaveChunk(ctx, docID, 1, save); err != nil {
		t.Fatalf("replayed chunk was not idempotent: %v", err)
	}
	changedReplay := save
	changedReplay.Notes = "Different notebook"
	if _, err := service.SaveChunk(ctx, docID, 1, changedReplay); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay must conflict: %v", err)
	}
	stale := save
	stale.FencingToken = "old"
	if _, err := service.SaveChunk(ctx, docID, 2, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale attempt accepted: %v", err)
	}
	if _, err := service.Finish(ctx, docID, execution, "no_changes"); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial reading marked complete: %v", err)
	}
	if _, err := service.Control(ctx, docID, caller.EntityID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveChunk(ctx, docID, 2, save); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled write accepted: %v", err)
	}
	if err := service.dispatchDocuments(ctx); err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Get(ctx, docID)
	if err != nil || cancelled.Document.Status != "cancelled" {
		t.Fatalf("cancel status: %+v %v", cancelled, err)
	}
	retry, err := service.Control(ctx, docID, caller.EntityID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Document.Generation != 2 || retry.Chunks[0].CompletedAt == nil || retry.Notes.Revision != 1 {
		t.Fatal("retry lost durable work")
	}
	generation = 2
	taskID = identifier.New(identifier.Prefix("task"))
	session = identifier.New(identifier.Session)
	status = "running"
	execution.Generation = 2
	if err := service.dispatchDocuments(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Prepare(ctx, docID, execution)
	if err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 1 {
		t.Fatal("retry repeated extraction")
	}
	for _, chunk := range resumed.Chunks[1:] {
		result, err := service.ReadChunk(ctx, docID, chunk.Number)
		if err != nil {
			t.Fatal(err)
		}
		sources = nil
		for _, page := range result["evidence"].([]DocumentEvidence) {
			sources = append(sources, page.SourceRecordID)
		}
		save = SaveDocumentChunk{DocumentExecution: execution, Revision: uint64(chunk.Number - 1), Findings: "Requirements or extraction uncertainty " + strings.Join(sources, " "), Notes: fmt.Sprintf("Revision %d retains source %s", chunk.Number, sources[0]), SourceRecordIDs: sources}
		invalid := save
		invalid.Notes += " " + identifier.New(identifier.SourceRecord)
		if _, err := service.SaveChunk(ctx, docID, chunk.Number, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invented notebook citation accepted: %v", err)
		}
		if _, err := service.SaveChunk(ctx, docID, chunk.Number, save); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Reconcile(ctx, docID, execution); err != nil {
		t.Fatal(err)
	}
	final, err := service.Finish(ctx, docID, execution, "no_changes")
	if err != nil || final.Document.Status != "no_changes" {
		t.Fatalf("completion: %+v %v", final, err)
	}
	var revisions int64
	if err := db.Model(&model.DocumentNotesRevision{}).Where("document_id = ?", docID).Count(&revisions).Error; err != nil || int(revisions) != len(final.Chunks) {
		t.Fatalf("notes history: %d %v", revisions, err)
	}
	original, _, err := service.ReadOriginal(ctx, docID)
	if err != nil || string(original) != string(data) {
		t.Fatalf("original: %v", err)
	}
	data = []byte("%PDF-replaced")
	if _, _, err := service.ReadOriginal(ctx, docID); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed original accepted: %v", err)
	}
}

func TestDocumentChunksBoundUTF8AndPreserveAllText(t *testing.T) {
	text := []string{strings.Repeat("規則", 20000), "", "next page"}
	pages := []model.DocumentPage{{Number: 1, SourceRecordID: "one"}, {Number: 2, SourceRecordID: "two"}, {Number: 3, SourceRecordID: "three"}}
	chunks := buildDocumentChunks("document", pages, text)
	reconstructed := map[int]string{}
	for _, chunk := range chunks {
		var pieces []DocumentEvidence
		if err := json.Unmarshal(chunk.Evidence, &pieces); err != nil {
			t.Fatal(err)
		}
		size := 0
		for _, piece := range pieces {
			if len(reconstructed[piece.Page]) != piece.Offset {
				t.Fatal("lost offset")
			}
			reconstructed[piece.Page] += piece.Content
			size += len(piece.Content)
		}
		if size > 16000 {
			t.Fatal("unbounded chunk")
		}
	}
	for i, original := range text {
		if reconstructed[i+1] != original {
			t.Fatal("lost page text")
		}
	}
}
