package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
)

type imageFixtureExtractor struct {
	fixtureExtractor
	rendered, recognized int
}

func (e *imageFixtureExtractor) RenderPage(context.Context, string, int) ([]byte, error) {
	e.rendered++
	var data bytes.Buffer
	err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 50, 100)), nil)
	return data.Bytes(), err
}
func (e *imageFixtureExtractor) RecognizeText(context.Context, []byte) (string, error) {
	e.recognized++
	return "F.1.13 Rollover Protection Envelope. The primary structure and planes between the Main Hoop and Front Hoop.", nil
}

func TestPageImagesOCRResumeVisualSearchAndAudit(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	renderer := &imageFixtureExtractor{fixtureExtractor: fixtureExtractor{pages: []string{""}}}
	service := NewDocumentService(db, t.TempDir(), renderer)
	id, runID, sid := identifier.New(identifier.Document), identifier.New(identifier.Run), identifier.New(identifier.Session)
	data := []byte("%PDF-test-" + id)
	hash := hashBytes(data)
	if err := os.WriteFile(filepath.Join(service.directory, hash+".pdf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	doc := model.Document{ID: id, SHA256: hash, Filename: "rules.pdf", Title: "Rules", Status: "queued", Generation: 1, SizeBytes: int64(len(data)), ReviewStatus: model.DocumentReviewPending, Tags: datatypes.NewJSONSlice([]string{}), RunID: runID}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/retrieval-context") {
			json.NewEncoder(w).Encode(map[string]any{"document_ids": []string{id}, "profile_id": "document_qa_v1"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"task_id": "task_test", "session_id": sid, "run_id": runID, "attempt_number": 1, "input": map[string]any{"document_id": id, "generation": 1}})
	}))
	defer runtime.Close()
	service.SetIntegration(nil, NewDocumentRuntimeClient(runtime.URL, "test"))
	execution := DocumentExecution{AttemptID: "attempt_test", WorkerID: "worker_test", FencingToken: "current", Generation: 1}
	prepared, err := service.Prepare(ctx, id, execution)
	if err != nil {
		t.Fatal(err)
	}
	if renderer.rendered != 1 || renderer.recognized != 1 || !prepared.ReadyForQuestions {
		t.Fatalf("OCR not prepared: %+v", prepared)
	}
	page, err := service.Page(ctx, id, 1)
	if err != nil || page.CaptureMethod != "document_ocr" || page.PageImage == nil || !strings.Contains(page.Content, "F.1.13") {
		t.Fatalf("OCR provenance: %+v %v", page, err)
	}
	if _, err := service.Prepare(ctx, id, execution); err != nil || renderer.rendered != 1 || renderer.recognized != 1 {
		t.Fatalf("retry redid extraction: %v", err)
	}
	resumed, err := service.preparePageImages(ctx, prepared.Document, hash, "unused", []string{""}, execution)
	if err != nil || resumed[0] != page.Content || renderer.rendered != 1 {
		t.Fatalf("image cache retry: %v", err)
	}
	reading := SaveDocumentChunk{DocumentExecution: execution, Findings: "Boundary per " + page.ID, Notes: "Rollover plane " + page.ID, SourceRecordIDs: []string{page.ID}, VisualFindings: []VisualFinding{{Page: 1, SectionIDs: []string{"F.1.13"}, Description: "The red dashed boundary follows the hoop outline in the front view."}}}
	saved, err := service.SaveChunk(ctx, id, 1, reading)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveChunk(ctx, id, 1, reading); err != nil {
		t.Fatalf("replayed visual findings: %v", err)
	}
	altered := reading
	altered.VisualFindings = []VisualFinding{{Page: 1, Description: "Changed interpretation."}}
	if _, err := service.SaveChunk(ctx, id, 1, altered); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	visualID := saved.Pages[0].VisualSourceRecordID
	record, err := NewSourceService(db).GetSourceRecord(ctx, visualID)
	if err != nil || record.EvidenceType != "agent_summary" || record.CaptureMethod != "document_vision" || record.ParentExternalID != "page:1" {
		t.Fatalf("visual provenance: %+v %v", record, err)
	}
	scope := DocumentRetrievalScope{DocumentIDs: []string{id}, AllowUnapproved: true}
	hits, err := service.SearchSources(ctx, DocumentSearchOptions{Query: "dashed", Limit: 8}, scope)
	if err != nil || len(hits.Results) != 1 || hits.Results[0].SourceRecordID != visualID || hits.Results[0].PDFPage != 1 || hits.Results[0].EvidenceType != "agent_summary" {
		t.Fatalf("visual search: %+v %v", hits, err)
	}
	team := DocumentRetrievalScope{Team: true}
	hits, err = service.SearchSources(ctx, DocumentSearchOptions{Query: "dashed", Limit: 8}, team)
	if err != nil || len(hits.Results) != 0 {
		t.Fatal("unapproved visual evidence leaked")
	}
	if err := service.AuthorizeImage(ctx, id, team); !errors.Is(err, ErrNotFound) {
		t.Fatal("unapproved image leaked")
	}
	qa, err := service.AgentImageScope(ctx, runID, sid)
	if err != nil {
		t.Fatal(err)
	}
	foreign := identifier.New(identifier.Document)
	if err := service.AuthorizeImage(ctx, foreign, qa); !errors.Is(err, ErrForbidden) {
		t.Fatal("QA image scope escaped")
	}
	if err := db.Model(&doc).Update("review_status", model.DocumentReviewIngested).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.AuthorizeImage(ctx, id, team); err != nil {
		t.Fatal(err)
	}
	hits, err = service.SearchSources(ctx, DocumentSearchOptions{Query: "dashed", Limit: 8}, team)
	if err != nil || len(hits.Results) != 1 {
		t.Fatal("approved visual evidence not shared")
	}
	citations, err := NewWikiService(db, nil).resolveCitations(ctx, []string{visualID})
	if err != nil || citations[visualID].DocumentID != id || citations[visualID].PDFPage != 1 {
		t.Fatalf("visual citation: %+v %v", citations, err)
	}
	if err := db.Model(&doc).Update("review_status", model.DocumentReviewRejected).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.AuthorizeImage(ctx, id, qa); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected image still exposed to QA")
	}
	if _, _, err := service.ReadPageImage(ctx, id, 1); err != nil {
		t.Fatalf("audit image lost: %v", err)
	}
	if err := os.WriteFile(filepath.Join(service.directory, "pages", page.PageImage.SHA256+".jpg"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadPageImage(ctx, id, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("image hash not enforced: %v", err)
	}
}

func TestPageImageBoundsAndOCRHeuristics(t *testing.T) {
	if !requiresOCR("") || !requiresOCR("A heading") || requiresOCR(strings.Repeat("A readable paragraph. ", 20)) {
		t.Fatal("OCR heuristic invalid")
	}
	if _, err := readLimitedImage(strings.NewReader(strings.Repeat("x", maxPageImageBytes+1))); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized image accepted")
	}
}

func TestPopplerPageRendering(t *testing.T) {
	path := os.Getenv("BLIX_TEST_PDF_PATH")
	if path == "" {
		t.Skip("BLIX_TEST_PDF_PATH is required")
	}
	data, err := (PopplerExtractor{}).RenderPage(context.Background(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || configuration.Width > 2400 || configuration.Height > 2400 || configuration.Width < 1 {
		t.Fatalf("rendered image invalid: %+v %v", configuration, err)
	}
}

func TestAgentImagesUseTrustedSingleDocumentScope(t *testing.T) {
	id := identifier.New(identifier.Document)
	runID, sid := identifier.New(identifier.Run), identifier.New(identifier.Session)
	for _, profile := range []string{"document_qa_v1", "document_ingest_v1", "blix_v1"} {
		t.Run(profile, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer internal" || r.URL.Query().Get("session_id") != sid {
					t.Error("untrusted image context")
				}
				json.NewEncoder(w).Encode(map[string]any{"document_ids": []string{id}, "profile_id": profile})
			}))
			defer server.Close()
			service := NewDocumentService(nil, t.TempDir(), nil)
			service.SetIntegration(nil, NewDocumentRuntimeClient(server.URL, "internal"))
			scope, err := service.AgentImageScope(context.Background(), runID, sid)
			if err != nil {
				t.Fatal(err)
			}
			if profile == "blix_v1" {
				if !scope.Team || scope.AllowUnapproved {
					t.Fatal("general agent obtained unapproved images")
				}
			} else if scope.Team || !scope.AllowUnapproved || len(scope.DocumentIDs) != 1 || scope.DocumentIDs[0] != id {
				t.Fatal("specialist escaped its bound document")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"document_ids": []string{id, identifier.New(identifier.Document)}, "profile_id": "document_qa_v1"})
	}))
	defer server.Close()
	service := NewDocumentService(nil, t.TempDir(), nil)
	service.SetIntegration(nil, NewDocumentRuntimeClient(server.URL, "internal"))
	if _, err := service.AgentImageScope(context.Background(), runID, sid); !errors.Is(err, ErrForbidden) {
		t.Fatalf("multi-document QA image scope: %v", err)
	}
}
