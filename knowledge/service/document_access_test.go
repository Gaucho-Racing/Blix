package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestQuestionAccessIsIndexReadyProfileBoundAndRevokedOnRejection(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	s := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: []string{"T.3.2 Brake requirements."}})
	details, err := s.Import(ctx, bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	id := details.Document.ID
	details, err = s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !details.ReadyForQuestions || details.Document.ReviewStatus != model.DocumentReviewInReview {
		t.Fatal("index readiness must not wait for approval")
	}
	if err := s.ValidateQuestionDocument(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRetrievalDocuments(ctx, []string{id}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("general chat accepted an unapproved document: %v", err)
	}
	runID, sessionID := identifier.New(identifier.Run), identifier.New(identifier.Session)
	var profile, bound atomic.Value
	profile.Store("blix_v1")
	bound.Store([]string{id})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session_id") != sessionID || r.Header.Get("Authorization") != "Bearer internal" {
			t.Error("scope lookup did not use trusted session credentials")
		}
		json.NewEncoder(w).Encode(map[string]any{"document_ids": bound.Load(), "profile_id": profile.Load()})
	}))
	defer server.Close()
	s.SetIntegration(nil, NewDocumentRuntimeClient(server.URL, "internal"))
	for _, ordinary := range []string{"blix_v1", "deep_research_v1", "knowledge_curator_v1", "document_ingest_v1"} {
		profile.Store(ordinary)
		scope, err := s.RunRetrievalScope(ctx, runID, sessionID, "run")
		if err != nil || scope.AllowUnapproved {
			t.Fatalf("ordinary profile obtained private indexes: %+v %v", scope, err)
		}
		hits, err := s.SearchSources(ctx, DocumentSearchOptions{Query: "brake", Limit: 8}, scope)
		if err != nil || len(hits.Results) != 0 {
			t.Fatalf("ordinary profile read private indexes: %+v %v", hits, err)
		}
	}
	profile.Store("document_qa_v1")
	scope, err := s.RunRetrievalScope(ctx, runID, sessionID, "team")
	if err != nil || !scope.AllowUnapproved || scope.Team || len(scope.DocumentIDs) != 1 {
		t.Fatalf("QA escaped its single document scope: %+v %v", scope, err)
	}
	hits, err := s.SearchSources(ctx, DocumentSearchOptions{Query: "brake", Limit: 8}, scope)
	if err != nil || len(hits.Results) != 1 {
		t.Fatalf("QA lost unapproved evidence: %+v %v", hits, err)
	}
	if err := s.AuthorizeAgentRecord(ctx, details.Pages[0].SourceRecordID, runID, sessionID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("QA bypassed indexed retrieval with a raw evidence read: %v", err)
	}
	profile.Store("knowledge_curator_v1")
	if err := s.AuthorizeAgentDocument(ctx, id, runID, sessionID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("general curator read unapproved document: %v", err)
	}
	profile.Store("document_ingest_v1")
	if err := s.AuthorizeAgentRecord(ctx, details.Pages[0].SourceRecordID, runID, sessionID); err != nil {
		t.Fatalf("ingestion lost its original evidence: %v", err)
	}
	bound.Store([]string{identifier.New(identifier.Document)})
	if err := s.AuthorizeAgentDocument(ctx, id, runID, sessionID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ingestion read a foreign document: %v", err)
	}
	profile.Store("document_qa_v1")
	bound.Store([]string{id, identifier.New(identifier.Document)})
	if _, err := s.RunRetrievalScope(ctx, runID, sessionID, "team"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("multi-document QA scope accepted: %v", err)
	}
	bound.Store([]string{id})
	recordID := details.Pages[0].SourceRecordID
	if err := db.Where("source_record_id = ?", recordID).Delete(&model.SourceRecordIndex{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateQuestionDocument(ctx, id); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete index accepted questions: %v", err)
	}
	if err := indexSourceRecord(db, model.SourceRecord{ID: recordID, Content: "T.3.2 Brake requirements."}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&details.Document).Update("review_status", model.DocumentReviewIngested).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRetrievalDocuments(ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeAgentRecord(ctx, recordID, "", ""); err != nil {
		t.Fatalf("approved evidence not shared: %v", err)
	}
	if err := db.Model(&details.Document).Update("review_status", model.DocumentReviewRejected).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateQuestionDocument(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected document accepted questions: %v", err)
	}
	hits, err = s.SearchSources(ctx, DocumentSearchOptions{Query: "brake", Limit: 8}, scope)
	if err != nil || len(hits.Results) != 0 {
		t.Fatalf("existing QA scope retained rejected evidence: %+v %v", hits, err)
	}
	if err := s.AuthorizeAgentRecord(ctx, recordID, runID, sessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected raw evidence remained readable: %v", err)
	}
	audit, err := s.Page(ctx, id, 1)
	if err != nil || audit.Content == "" {
		t.Fatalf("rejection deleted human audit evidence: %+v %v", audit, err)
	}
	final, err := s.Get(ctx, id)
	if err != nil || final.ReadyForQuestions {
		t.Fatalf("rejected document still advertised Q&A: %+v %v", final, err)
	}
}
