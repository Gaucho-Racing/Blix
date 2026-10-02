package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestExtractionCheckpointsAreFencedAndFeedReader(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	id := identifier.New(identifier.Document)
	parent := identifier.New(identifier.Prefix("task"))
	child := identifier.New(identifier.Prefix("task"))
	run := identifier.New(identifier.Run)
	session := identifier.New(identifier.Session)
	var profile atomic.Value
	profile.Store("file_ingest_v1")
	var attempt atomic.Uint64
	attempt.Store(1)
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request DocumentExecution
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.FencingToken != "current" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		taskID := child
		if profile.Load() == "document_ingest_v1" {
			taskID = parent
		}
		json.NewEncoder(w).Encode(map[string]any{"profile_id": profile.Load(), "task_id": taskID, "parent_task_id": parent, "run_id": run, "session_id": session, "attempt_number": attempt.Load(), "input": map[string]any{"document_id": id, "generation": 1}})
	}))
	defer runtime.Close()
	service := NewDocumentService(db, t.TempDir(), nil)
	service.SetIntegration(nil, NewDocumentRuntimeClient(runtime.URL, "internal"))
	doc := model.Document{ID: id, Filename: "test.pdf", Title: "test", Generation: 1, Status: "queued", ReviewStatus: model.DocumentReviewPending}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	execution := DocumentExecution{AttemptID: "attempt", WorkerID: "worker", FencingToken: "current", Generation: 1}
	pdfHash := hashBytes([]byte("%PDF-test-" + id))
	start := ExtractionStart{DocumentExecution: execution, SHA256: pdfHash, SizeBytes: 9, PageCount: 2, Version: 1}
	if err := service.StartExtraction(ctx, id, start); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteExtraction(ctx, id, execution); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete file accepted: %v", err)
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 10, 20)), nil); err != nil {
		t.Fatal(err)
	}
	page := ExtractionPage{DocumentExecution: execution, NativeText: "native", OCRText: "OCR braking requirements", Text: "OCR braking requirements", TextMethod: "ocr", SHA256: hashBytes(data.Bytes()), Width: 10, Height: 20, ImageBase64: base64.StdEncoding.EncodeToString(data.Bytes())}
	if err := service.SaveExtractionPage(ctx, id, 1, page); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveExtractionPage(ctx, id, 1, page); err != nil {
		t.Fatalf("duplicate callback failed: %v", err)
	}
	var asset model.DocumentPageImage
	if err := db.First(&asset, "document_id = ? AND number = 1", id).Error; err != nil || asset.NativeText != "native" || asset.OCRText != page.OCRText {
		t.Fatalf("raw text not retained: %+v %v", asset, err)
	}
	manifest, err := service.ExtractionManifest(ctx, id, execution)
	if err != nil || len(manifest.CompletedPages) != 1 || manifest.CompletedPages[0] != 1 {
		t.Fatalf("checkpoint not returned: %+v %v", manifest, err)
	}
	stale := page
	stale.FencingToken = "stale"
	if err := service.SaveExtractionPage(ctx, id, 2, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker wrote a page: %v", err)
	}
	attempt.Store(2)
	if _, err := service.ExtractionManifest(ctx, id, execution); err != nil {
		t.Fatal(err)
	}
	attempt.Store(1)
	if err := service.SaveExtractionPage(ctx, id, 2, page); !errors.Is(err, ErrConflict) {
		t.Fatalf("older attempt wrote a page: %v", err)
	}
	attempt.Store(2)
	if err := service.SaveExtractionPage(ctx, id, 2, page); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteExtraction(ctx, id, execution); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteExtraction(ctx, id, execution); err != nil {
		t.Fatalf("duplicate completion failed: %v", err)
	}
	details, err := service.Get(ctx, id)
	if err != nil || len(details.Pages) != 2 || len(details.Chunks) != 1 || details.Document.SourceID == "" || !documentStageComplete(details.Document, "extracting") {
		t.Fatalf("reader evidence missing: %+v %v", details, err)
	}
	if _, err := service.Prepare(ctx, id, execution); !errors.Is(err, ErrForbidden) {
		t.Fatalf("extraction worker entered reader: %v", err)
	}
	profile.Store("document_ingest_v1")
	prepared, err := service.Prepare(ctx, id, execution)
	if err != nil || !prepared.ReadyForQuestions || prepared.Document.ReviewStatus != model.DocumentReviewPending {
		t.Fatalf("reader handoff failed: %+v %v", prepared, err)
	}
	if err := service.SaveExtractionPage(ctx, id, 2, page); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader wrote extraction data: %v", err)
	}
	if err := db.Model(&doc).Update("review_status", model.DocumentReviewRejected).Error; err != nil {
		t.Fatal(err)
	}
	profile.Store("file_ingest_v1")
	if _, err := service.ExtractionManifest(ctx, id, execution); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected document allowed extraction: %v", err)
	}
}
