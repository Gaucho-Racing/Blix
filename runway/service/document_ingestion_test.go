package service

import (
	"encoding/json"
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
	if err := f.db.First(&attempt, "task_id = ?", first.ExtractionTaskID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.workers.Register(f.ctx, WorkerRegistrationRequest{WorkerID: f.worker, RuntimeKind: "ingestion", Concurrency: 2, BackgroundConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.s.ClaimAttempt(f.ctx, attempt.ID, ClaimRequest{WorkerID: f.worker, RuntimeKind: "ingestion"})
	if err != nil {
		t.Fatal(err)
	}
	access := DocumentAttemptAccess{AttemptID: attempt.ID, WorkerID: f.worker, FencingToken: claimed.Attempt.FencingToken}
	valid, err := f.s.ValidateDocumentAttempt(f.ctx, access)
	if err != nil || valid["task_id"] != first.ExtractionTaskID {
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
	if err := f.db.First(&next, "task_id = ? AND status = ?", first.ExtractionTaskID, model.StatusQueued).Error; err != nil {
		t.Fatal(err)
	}
	resumed, err := f.s.ClaimAttempt(f.ctx, next.ID, ClaimRequest{WorkerID: f.worker, RuntimeKind: "ingestion"})
	if err != nil {
		t.Fatal(err)
	}
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

func TestExtractionTaskGatesDocumentReader(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%v", success), func(t *testing.T) {
			f := setup(t)
			request := DocumentIngestionRequest{Caller: f.owner, DocumentID: identifier.New(identifier.Prefix("document")), Generation: 1, Objective: "Read the rules"}
			request.Key = fmt.Sprintf("%s:1", request.DocumentID)
			run, err := f.s.StartDocumentIngestion(f.ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			parent, err := f.s.GetTask(f.ctx, run.TaskID)
			if err != nil || parent.Status != model.StatusWaiting {
				t.Fatalf("reader ran before extraction: %+v %v", parent, err)
			}
			var count int64
			f.db.Model(&model.ExecutionAttempt{}).Where("task_id = ?", run.TaskID).Count(&count)
			if count != 0 {
				t.Fatal("reader attempt was queued early")
			}
			var attempt model.ExecutionAttempt
			if err := f.db.First(&attempt, "task_id = ?", run.ExtractionTaskID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ClaimAttempt(f.ctx, attempt.ID, ClaimRequest{WorkerID: f.worker, RuntimeKind: "ingestion"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("API worker claimed extraction: %v", err)
			}
			ready, err := f.s.ReadyAttempts(f.ctx, "ingestion", 20)
			if err != nil || len(ready) != 1 || ready[0].ID != attempt.ID {
				t.Fatalf("ready queue: %+v %v", ready, err)
			}
			extractor := identifier.New(identifier.Worker)
			if _, err := f.s.workers.Register(f.ctx, WorkerRegistrationRequest{WorkerID: extractor, RuntimeKind: "ingestion", Concurrency: 2, BackgroundConcurrency: 2}); err != nil {
				t.Fatal(err)
			}
			claimed, err := f.s.ClaimAttempt(f.ctx, attempt.ID, ClaimRequest{WorkerID: extractor, RuntimeKind: "ingestion"})
			if err != nil {
				t.Fatal(err)
			}
			if success {
				f.completeTask(t, claimed)
			} else {
				if err := f.s.Fail(f.ctx, attempt.ID, FailureRequest{Ownership: own(claimed), Error: json.RawMessage(encoded(map[string]string{"code": "invalid_pdf", "message": "Invalid PDF"})), Retryable: false}); err != nil {
					t.Fatal(err)
				}
			}
			parent, err = f.s.GetTask(f.ctx, run.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			f.db.Model(&model.ExecutionAttempt{}).Where("task_id = ?", run.TaskID).Count(&count)
			if success {
				if parent.Status != model.StatusQueued || parent.WaitingTaskID != nil || count != 1 {
					t.Fatalf("reader not released: %+v, attempts %d", parent, count)
				}
				reader := f.taskAttempt(t, run.TaskID)
				if reader.Session.ProfileID != "document_ingest_v1" || len(reader.ResumeResult) > 0 {
					t.Fatal("reader resumed with invalid extraction payload")
				}
			} else if parent.Status != model.StatusFailed || count != 0 {
				t.Fatalf("failed extraction queued reader: %+v, attempts %d", parent, count)
			}
		})
	}
}
