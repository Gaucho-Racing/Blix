package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

func TestDocumentSchedulingIsIdempotentAndFenced(t *testing.T) {
	f := setup(t)
	request := DocumentIngestionRequest{Key: "document-job", Caller: f.owner, DocumentID: identifier.New(identifier.Prefix("document")), Generation: 1, Objective: "Read the official rules and draft wiki changes."}
	request.Key = fmt.Sprintf("%s:%d", request.DocumentID, request.Generation)
	first, err := f.s.StartDocumentIngestion(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.StartDocumentIngestion(f.ctx, request)
	if err != nil || again != first {
		t.Fatalf("duplicate scheduling: %+v %v", again, err)
	}
	altered := request
	altered.Objective = "Different objective"
	if _, err := f.s.StartDocumentIngestion(f.ctx, altered); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused key accepted conflicting input: %v", err)
	}
	var attempt model.ExecutionAttempt
	if err := f.db.First(&attempt, "task_id = ?", first.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	claimed := f.claim(t, attempt.ID)
	access := DocumentAttemptAccess{AttemptID: attempt.ID, WorkerID: f.worker, FencingToken: claimed.Attempt.FencingToken}
	valid, err := f.s.ValidateDocumentAttempt(f.ctx, access)
	if err != nil || valid["task_id"] != first.TaskID {
		t.Fatalf("access: %+v %v", valid, err)
	}
	if err := f.db.Model(&attempt).Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ValidateDocumentAttempt(f.ctx, access); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired lease accepted: %v", err)
	}
	if err := f.s.RecoverExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	var next model.ExecutionAttempt
	if err := f.db.First(&next, "task_id = ? AND status = ?", first.TaskID, model.StatusQueued).Error; err != nil {
		t.Fatal(err)
	}
	resumed := f.claim(t, next.ID)
	if resumed.Attempt.Number != 2 {
		t.Fatal("missing retry attempt")
	}
	if _, err := f.s.ValidateDocumentAttempt(f.ctx, access); !errors.Is(err, ErrConflict) {
		t.Fatalf("old worker accepted: %v", err)
	}
	if err := f.s.ControlTask(f.ctx, first.TaskID, f.owner.EntityID, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	newer := DocumentAttemptAccess{AttemptID: next.ID, WorkerID: f.worker, FencingToken: resumed.Attempt.FencingToken}
	if _, err := f.s.ValidateDocumentAttempt(f.ctx, newer); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled task accepted: %v", err)
	}
}
