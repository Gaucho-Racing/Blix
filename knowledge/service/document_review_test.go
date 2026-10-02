package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/depot"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func documentReviewFixture(t *testing.T, db *gorm.DB) (*DocumentService, DocumentDetails) {
	t.Helper()
	s := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: []string{"The official brake requirements specify two hydraulic circuits."}})
	doc, err := s.Import(context.Background(), bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&doc.Document).Updates(map[string]any{"status": "ready", "session_id": identifier.New(identifier.Session), "conversation_id": identifier.New(identifier.Conversation), "generation": 1}).Error; err != nil {
		t.Fatal(err)
	}
	doc, err = s.Get(context.Background(), doc.Document.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, doc
}

func documentChangesetFixture(t *testing.T, s *WikiService, doc DocumentDetails) ChangesetDetails {
	t.Helper()
	head := strings.Repeat("a", 40)
	source := doc.Pages[0].SourceRecordID
	result, err := s.CreateChangeset(context.Background(), CreateChangesetInput{ConversationID: doc.Document.ConversationID, SessionID: doc.Document.SessionID, Type: model.ChangesetTypeContent, Summary: "Update braking requirements", BaseCommitSHA: &head, SourceRecordIDs: []string{source}, Files: []repository.Edit{{Operation: "upsert", Path: "brakes.md", Content: "# Brakes\n\nTwo hydraulic circuits are required. " + source}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Documents) != 1 || result.Documents[0].ID != doc.Document.ID {
		t.Fatalf("document relationship missing: %+v", result.Documents)
	}
	return result
}

func reviewCaller(admin bool) CallerContext {
	group := "BlixUsers"
	if admin {
		group = "BlixAdmins"
	}
	return CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", Groups: []string{group}}
}

func TestDocumentApprovalIsSeparateFromWikiPublication(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	repo := &publishingRepository{}
	wiki := NewWikiService(db, repo)
	changeset := documentChangesetFixture(t, wiki, doc)
	user, admin := reviewCaller(false), reviewCaller(true)
	ctx := context.Background()
	for _, tc := range []struct {
		caller   CallerContext
		decision string
	}{
		{user, "approve"},
		{CallerContext{EntityID: user.EntityID, EntityType: "USER"}, "reject"},
		{CallerContext{EntityID: user.EntityID, EntityType: "SERVICE_ACCOUNT", Groups: []string{"BlixAdmins"}}, "approve"},
		{CallerContext{EntityID: user.EntityID, EntityType: "SERVICE_ACCOUNT", Groups: []string{"BlixUsers"}}, "reject"},
	} {
		if _, err := s.Review(ctx, doc.Document.ID, tc.decision, ReviewInput{Caller: tc.caller, Note: "Review"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unauthorized document review: %v", err)
		}
	}
	if _, err := wiki.ApplyChangeset(ctx, changeset.Changeset.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("automatic publication accepted an unreviewed document: %v", err)
	}
	if _, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: user, Note: " "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank rejection accepted: %v", err)
	}
	approved, err := s.Review(ctx, doc.Document.ID, "approve", ReviewInput{Caller: admin, Note: "  Official reference.  "})
	if err != nil || approved.Document.ReviewStatus != model.DocumentReviewIngested || approved.Document.ReviewedBy != admin.EntityID || approved.Document.ReviewNote != "Official reference." || approved.Document.ReviewedAt == nil {
		t.Fatalf("document approval: %+v %v", approved.Document, err)
	}
	if approved.Changesets[0].Status != model.ChangesetStatusProposed || repo.prepared != 0 || repo.published != 0 {
		t.Fatal("document approval published its wiki edits")
	}
	replay, err := s.Review(ctx, doc.Document.ID, "approve", ReviewInput{Caller: reviewCaller(true), Note: "Another review"})
	if err != nil || replay.Document.ReviewedBy != admin.EntityID || replay.Document.ReviewNote != approved.Document.ReviewNote {
		t.Fatal("approval replay changed original review")
	}
	if _, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: user, Note: "Changed my mind"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal approval reversed: %v", err)
	}
	sources, err := NewSourceService(db).FindSources(ctx, SourceQuery{ExternalID: doc.Document.SHA256, Limit: 5})
	if err != nil || len(sources) != 1 || sources[0].ID != doc.Document.SourceID {
		t.Fatalf("accepted document missing from source discovery: %+v %v", sources, err)
	}
}

func TestDocumentRejectionCascadesAndPreservesEvidence(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	wiki := NewWikiService(db, &publishingRepository{})
	first := documentChangesetFixture(t, wiki, doc)
	second := documentChangesetFixture(t, wiki, doc)
	third := documentChangesetFixture(t, wiki, doc)
	if err := db.Model(&model.Changeset{}).Where("id = ?", third.Changeset.ID).Update("status", model.ChangesetStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	_, unrelatedDoc := documentReviewFixture(t, db)
	unrelated := documentChangesetFixture(t, wiki, unrelatedDoc)
	user := reviewCaller(false)
	ctx := context.Background()
	if _, err := wiki.ReviewChangeset(ctx, first.Changeset.ID, "reject", ReviewInput{Caller: user, Note: "Decline these edits."}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Get(ctx, doc.Document.ID)
	if err != nil || pending.Document.ReviewStatus != model.DocumentReviewInReview {
		t.Fatal("changeset rejection rejected its document")
	}
	rejected, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: user, Note: "Unrelated to Gaucho Racing."})
	if err != nil || rejected.Document.ReviewStatus != model.DocumentReviewRejected || rejected.Document.ReviewedBy != user.EntityID {
		t.Fatalf("document rejection failed: %+v %v", rejected, err)
	}
	for _, changeset := range rejected.Changesets {
		if changeset.Status != model.ChangesetStatusRejected {
			t.Fatalf("open changeset escaped cascade: %+v", changeset)
		}
		if changeset.ID == first.Changeset.ID {
			if changeset.ReviewNote != "Decline these edits." || changeset.RejectedDocumentID != "" {
				t.Fatal("cascade overwrote previous rejection")
			}
		} else if changeset.RejectedDocumentID != doc.Document.ID || changeset.ReviewedBy != user.EntityID || changeset.ReviewNote != rejected.Document.ReviewNote {
			t.Fatal("cascade did not record source document and reviewer")
		}
	}
	untouched, err := wiki.GetChangeset(ctx, unrelated.Changeset.ID)
	if err != nil || untouched.Changeset.Status != model.ChangesetStatusProposed {
		t.Fatal("rejection affected an unrelated changeset")
	}
	if _, err := os.Stat(filepath.Join(s.directory, doc.Document.SHA256+".pdf")); err != nil {
		t.Fatal("original PDF was removed from review evidence")
	}
	var record model.SourceRecord
	if err := db.First(&record, "id = ?", doc.Pages[0].SourceRecordID).Error; err != nil || record.Content == "" {
		t.Fatal("page evidence was removed from review record")
	}
	if _, err := NewSourceService(db).GetSourceRecord(ctx, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected source available to agent evidence reads: %v", err)
	}
	sources, err := NewSourceService(db).FindSources(ctx, SourceQuery{ExternalID: doc.Document.SHA256, Limit: 5})
	if err != nil || len(sources) != 0 {
		t.Fatal("rejected document discoverable as team knowledge")
	}
	for _, id := range []string{second.Changeset.ID, third.Changeset.ID} {
		if _, err := wiki.ReviewChangeset(ctx, id, "approve", ReviewInput{Caller: reviewCaller(true)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("cascaded changeset published: %v", err)
		}
	}
	head := strings.Repeat("a", 40)
	if _, err := wiki.CreateChangeset(ctx, CreateChangesetInput{ConversationID: identifier.New(identifier.Conversation), SessionID: identifier.New(identifier.Session), Type: model.ChangesetTypeContent, Summary: "Reuse rejected evidence", BaseCommitSHA: &head, SourceRecordIDs: []string{record.ID}, Files: []repository.Edit{{Operation: "upsert", Path: "brakes.md", Content: record.ID}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected evidence reused: %v", err)
	}
	if _, err := s.Control(ctx, doc.Document.ID, doc.Document.UploadedBy, "retry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected document restarted: %v", err)
	}
	replay, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(true), Note: "Another reason"})
	if err != nil || replay.Document.ReviewedBy != user.EntityID || replay.Document.ReviewNote != rejected.Document.ReviewNote {
		t.Fatal("rejection replay overwrote the audit")
	}
	if _, err := s.Review(ctx, doc.Document.ID, "approve", ReviewInput{Caller: reviewCaller(true)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected document accepted: %v", err)
	}
}

func TestChangesetPublicationAcceptsLinkedDocumentsOnlyAfterSuccess(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, first := documentReviewFixture(t, db)
	_, second := documentReviewFixture(t, db)
	repo := &publishingRepository{publishErr: errors.New("publication failed")}
	wiki := NewWikiService(db, repo)
	head := strings.Repeat("a", 40)
	sources := []string{first.Pages[0].SourceRecordID, second.Pages[0].SourceRecordID}
	changeset, err := wiki.CreateChangeset(context.Background(), CreateChangesetInput{ConversationID: first.Document.ConversationID, SessionID: first.Document.SessionID, Type: model.ChangesetTypeContent, Summary: "Cross-reference requirements", BaseCommitSHA: &head, SourceRecordIDs: sources, Files: []repository.Edit{{Operation: "upsert", Path: "brakes.md", Content: strings.Join(sources, " ")}}})
	if err != nil || len(changeset.Documents) != 2 {
		t.Fatalf("multi-document provenance: %+v %v", changeset.Documents, err)
	}
	admin := reviewCaller(true)
	ctx := context.Background()
	if _, err := wiki.ReviewChangeset(ctx, changeset.Changeset.ID, "approve", ReviewInput{Caller: admin, Note: "Verified requirements."}); err == nil {
		t.Fatal("failed publication accepted")
	}
	for _, doc := range []DocumentDetails{first, second} {
		pending, err := s.Get(ctx, doc.Document.ID)
		if err != nil || pending.Document.ReviewStatus != model.DocumentReviewInReview || !pending.ReviewBlocked {
			t.Fatal("failed publication accepted the document or lost recovery protection")
		}
		if _, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Reject"}); !errors.Is(err, ErrConflict) {
			t.Fatal("uncertain publication was rejected")
		}
	}
	repo.publishErr = nil
	published, err := wiki.ReviewChangeset(ctx, changeset.Changeset.ID, "approve", ReviewInput{Caller: admin, Note: "Verified requirements."})
	if err != nil || published.Changeset.Status != model.ChangesetStatusCommitted || len(published.Documents) != 2 {
		t.Fatalf("publication retry: %+v %v", published, err)
	}
	for _, doc := range published.Documents {
		if doc.ReviewStatus != model.DocumentReviewIngested || doc.ReviewedBy != admin.EntityID || doc.ReviewNote != "Verified requirements." || doc.ReviewedAt == nil {
			t.Fatal("publication did not atomically accept its source documents")
		}
	}
}

func TestDocumentPublicationLeaseBlocksConcurrentRejection(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	repo := &publishingRepository{started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(repo.release) }) }
	t.Cleanup(release)
	wiki := NewWikiService(db, repo)
	changeset := documentChangesetFixture(t, wiki, doc)
	done := make(chan error, 1)
	go func() {
		_, err := wiki.ReviewChangeset(context.Background(), changeset.Changeset.ID, "approve", ReviewInput{Caller: reviewCaller(true)})
		done <- err
	}()
	select {
	case <-repo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not start")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '200ms'").Error; err != nil {
			return err
		}
		var stored model.Document
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stored, "id = ?", doc.Document.ID).Error
	}); err != nil {
		t.Fatalf("publication held document lock across network operation: %v", err)
	}
	if _, err := s.Review(context.Background(), doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Reject"}); !errors.Is(err, ErrConflict) {
		t.Fatal("in-flight publication allowed document rejection")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Get(context.Background(), doc.Document.ID)
	if err != nil || accepted.Document.ReviewStatus != model.DocumentReviewIngested {
		t.Fatal("publication did not complete acceptance")
	}
}

func TestRejectedDocumentFencesRunningIngestion(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	wiki := NewWikiService(db, &publishingRepository{})
	changeset := documentChangesetFixture(t, wiki, doc)
	if err := db.Model(&doc.Document).Updates(map[string]any{"status": "reading", "task_id": identifier.New(identifier.Prefix("task"))}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Review(ctx, doc.Document.ID, "approve", ReviewInput{Caller: reviewCaller(true)}); !errors.Is(err, ErrConflict) {
		t.Fatal("unfinished reading was accepted")
	}
	if _, err := wiki.ReviewChangeset(ctx, changeset.Changeset.ID, "approve", ReviewInput{Caller: reviewCaller(true)}); !errors.Is(err, ErrConflict) {
		t.Fatal("unfinished document changeset published")
	}
	rejected, err := s.Review(ctx, doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Wrong document"})
	if err != nil || rejected.Document.Generation != doc.Document.Generation+1 || rejected.Document.Status != "cancelling" {
		t.Fatalf("running ingestion not fenced: %+v %v", rejected.Document, err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := s.lockedExecution(ctx, tx, doc.Document.ID, DocumentExecution{Generation: doc.Document.Generation})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected document accepted stale worker: %v", err)
	}
}

func TestDocumentRejectionDuringUploadCompletionDoesNotQueueWork(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	if err := db.Model(&doc.Document).Updates(map[string]any{"status": "uploading", "depot_file_id": "file_test"}).Error; err != nil {
		t.Fatal(err)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			close(started)
			<-resume
			json.NewEncoder(w).Encode(map[string]any{"id": "file_test", "status": "ACTIVE", "size_bytes": 100})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(storage.Close)
	t.Cleanup(release)
	s.SetIntegration(depot.NewClient(storage.URL, "documents", "token"), nil)
	done := make(chan error, 1)
	go func() {
		_, err := s.CompleteUpload(context.Background(), doc.Document.ID, doc.Document.UploadedBy)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not start")
	}
	if _, err := s.Review(context.Background(), doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Wrong upload"}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rejected, err := s.Get(context.Background(), doc.Document.ID)
	if err != nil || rejected.Document.ReviewStatus != model.DocumentReviewRejected || rejected.Document.Status != "cancelled" {
		t.Fatal("late upload completion requeued rejected document")
	}
}

func TestDocumentRejectionDuringSchedulingRetainsCancellationForRetry(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, doc := documentReviewFixture(t, db)
	if err := db.Model(&doc.Document).Updates(map[string]any{"status": "queued", "task_id": ""}).Error; err != nil {
		t.Fatal(err)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	task := identifier.New(identifier.Prefix("task"))
	var allowCancel atomic.Bool
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/internal/v1/document-ingestions":
			close(started)
			<-resume
			json.NewEncoder(w).Encode(map[string]string{"task_id": task, "conversation_id": doc.Document.ConversationID})
		case r.URL.Path == "/internal/v1/tasks/"+task+"/cancel":
			if !allowCancel.Load() {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(200)
		case r.URL.Path == "/internal/v1/tasks/"+task:
			json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(runtime.Close)
	t.Cleanup(release)
	s.SetIntegration(nil, NewDocumentRuntimeClient(runtime.URL, "token"))
	done := make(chan error, 1)
	go func() { done <- s.dispatchDocuments(context.Background()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduling did not start")
	}
	if _, err := s.Review(context.Background(), doc.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Unrelated document"}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cancellation failure not reported: %v", err)
	}
	rejected, err := s.Get(context.Background(), doc.Document.ID)
	if err != nil || rejected.Document.ReviewStatus != model.DocumentReviewRejected || rejected.Document.Status != "cancelling" || rejected.Document.TaskID != task {
		t.Fatal("scheduled task lost its durable cancellation")
	}
	allowCancel.Store(true)
	if err := s.dispatchDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	rejected, err = s.Get(context.Background(), doc.Document.ID)
	if err != nil || rejected.Document.ReviewStatus != model.DocumentReviewRejected || rejected.Document.Status != "cancelled" {
		t.Fatal("scheduled task cancellation did not recover")
	}
}

func TestChangesetTracksDocumentCitationsBeyondSuppliedEvidence(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	s, accepted := documentReviewFixture(t, db)
	_, pending := documentReviewFixture(t, db)
	ctx := context.Background()
	if _, err := s.Review(ctx, accepted.Document.ID, "approve", ReviewInput{Caller: reviewCaller(true)}); err != nil {
		t.Fatal(err)
	}
	wiki := NewWikiService(db, &publishingRepository{})
	head := strings.Repeat("a", 40)
	input := CreateChangesetInput{ConversationID: identifier.New(identifier.Conversation), SessionID: identifier.New(identifier.Session), Type: model.ChangesetTypeContent, Summary: "Retain earlier citations", BaseCommitSHA: &head, SourceRecordIDs: []string{accepted.Pages[0].SourceRecordID}, Files: []repository.Edit{{Operation: "upsert", Path: "brakes.md", Content: accepted.Pages[0].SourceRecordID + " " + pending.Pages[0].SourceRecordID}}}
	changeset, err := wiki.CreateChangeset(ctx, input)
	if err != nil || len(changeset.Documents) != 2 || len(changeset.Citations) != 2 {
		t.Fatalf("retained citation not linked to its document: %+v %v", changeset, err)
	}
	if _, err := wiki.ApplyChangeset(ctx, changeset.Changeset.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("retained citation bypassed document review: %v", err)
	}
	if _, err := s.Review(ctx, pending.Document.ID, "reject", ReviewInput{Caller: reviewCaller(false), Note: "Reject the source"}); err != nil {
		t.Fatal(err)
	}
	rejected, err := wiki.GetChangeset(ctx, changeset.Changeset.ID)
	if err != nil || rejected.Changeset.Status != model.ChangesetStatusRejected {
		t.Fatal("citation-linked changeset escaped rejection")
	}
	if _, err := wiki.CreateChangeset(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("retained rejected citation reused: %v", err)
	}
}
