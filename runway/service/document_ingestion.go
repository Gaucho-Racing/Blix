package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type DocumentIngestionRequest struct {
	Title      string              `json:"title"`
	Key        string              `json:"key"`
	Caller     model.CallerContext `json:"caller"`
	DocumentID string              `json:"document_id"`
	Generation uint64              `json:"generation"`
	Objective  string              `json:"objective"`
}
type DocumentIngestionRun struct {
	RunID            string `json:"run_id"`
	TaskID           string `json:"task_id"`
	ExtractionTaskID string `json:"extraction_task_id"`
}

func (s *ExecutionService) StartDocumentIngestion(ctx context.Context, r DocumentIngestionRequest) (DocumentIngestionRun, error) {
	if len(r.Title) > 512 || strings.ContainsAny(r.Title, "\x00\r\n") || r.Key != fmt.Sprintf("%s:%d", r.DocumentID, r.Generation) || !identifier.IsValid(r.DocumentID, identifier.Prefix("document")) || r.Generation == 0 || strings.TrimSpace(r.Objective) == "" || len(r.Objective) > 4000 || !utf8.ValidString(r.Objective) || strings.ContainsRune(r.Objective, 0) || !identifier.IsValid(r.Caller.EntityID, identifier.Entity) || r.Caller.EntityType != "USER" || !identifier.IsValid(r.Caller.UserID, identifier.User) {
		return DocumentIngestionRun{}, ErrInvalid
	}
	var result DocumentIngestionRun
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "document-ingestion:"+r.Key).Error; err != nil {
			return err
		}
		var existing model.Task
		if err := tx.First(&existing, "parent_target_id = ? AND delegation_ref = ?", "document-ingestion", r.Key).Error; err == nil {
			var run model.Run
			if err := tx.First(&run, "id = ?", existing.RunID).Error; err != nil {
				return err
			}
			if run.CallerEntityID != r.Caller.EntityID || existing.Objective != r.Objective || !sameDocumentInvocation(json.RawMessage(existing.Input), r.DocumentID, r.Generation) {
				return ErrConflict
			}
			var extraction model.Task
			if err := tx.First(&extraction, "parent_target_id = ? AND delegation_ref = ?", existing.ID, "file-ingestion").Error; err != nil {
				return err
			}
			result = DocumentIngestionRun{existing.RunID, existing.ID, extraction.ID}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		runID := identifier.New(identifier.Run)
		lead, err := s.createSession(tx, runID, nil, profiles["blix_v1"], "")
		if err != nil {
			return err
		}
		name := "Document ingestion"
		if r.Title != "" {
			name = "Ingest: " + r.Title
		}
		runes := []rune(name)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		c := model.Run{ID: runID, Kind: model.RunKindInternal, Name: string(runes), CallerEntityID: r.Caller.EntityID, CallerContext: datatypes.NewJSONType(r.Caller), LeadSessionID: lead.ID, Status: "active", DocumentIDs: datatypes.NewJSONSlice([]string{r.DocumentID})}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		profile := profiles["document_ingest_v1"]
		session, err := s.createSession(tx, runID, &lead.ID, profile, "")
		if err != nil {
			return err
		}
		input := encoded(map[string]any{"document_id": r.DocumentID, "generation": r.Generation})
		invocation := encoded(map[string]any{"run_id": runID, "session_id": session.ID, "parent_session_id": lead.ID, "agent_id": profile.ID, "objective": r.Objective, "input": json.RawMessage(input)})
		task := model.Task{ID: identifier.New(identifier.Task), RunID: runID, ParentSessionID: lead.ID, SessionID: session.ID, ParentTargetID: "document-ingestion", DelegationRef: r.Key, Background: true, Objective: r.Objective, Input: input, Invocation: invocation, Status: model.StatusWaiting, WaitingReason: "file_ingestion", Policy: datatypes.NewJSONType(profile.Policy), DeadlineAt: time.Now().UTC().Add(time.Duration(profile.Policy.DeadlineSeconds) * time.Second)}
		extractionProfile := profiles["file_ingest_v1"]
		extractionSession, err := s.createSession(tx, runID, &session.ID, extractionProfile, "")
		if err != nil {
			return err
		}
		extraction := model.Task{ID: identifier.New(identifier.Task), RunID: runID, ParentTaskID: &task.ID, ParentSessionID: session.ID, SessionID: extractionSession.ID, ParentTargetID: task.ID, DelegationRef: "file-ingestion", Objective: "Extract text and page evidence from the uploaded file.", Input: input, Status: model.StatusQueued, Policy: datatypes.NewJSONType(extractionProfile.Policy), DeadlineAt: task.DeadlineAt}
		extraction.Invocation = encoded(map[string]any{"run_id": runID, "session_id": extraction.SessionID, "parent_session_id": session.ID, "agent_id": extractionProfile.ID, "objective": extraction.Objective, "input": json.RawMessage(input)})
		task.WaitingTaskID = &extraction.ID
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		if err := tx.Create(&extraction).Error; err != nil {
			return err
		}
		if _, err := s.newAttempt(tx, runID, extraction.SessionID, extraction.ID, nil, &extraction.ID, extractionProfile.Policy, extraction.DeadlineAt); err != nil {
			return err
		}
		if _, err := s.event(tx, model.ExecutionAttempt{RunID: runID, SessionID: lead.ID}, "task.created_v1", session.ID, map[string]any{"task_id": task.ID, "agent_id": profile.ID, "objective": task.Objective, "background": true}); err != nil {
			return err
		}
		if _, err := s.event(tx, model.ExecutionAttempt{RunID: runID, SessionID: session.ID}, "task.created_v1", extraction.SessionID, map[string]any{"task_id": extraction.ID, "agent_id": extractionProfile.ID, "objective": extraction.Objective, "background": false}); err != nil {
			return err
		}
		result = DocumentIngestionRun{runID, task.ID, extraction.ID}
		return nil
	})
	return result, err
}

type DocumentAttemptAccess struct {
	AttemptID    string `json:"attempt_id"`
	WorkerID     string `json:"worker_id"`
	FencingToken string `json:"fencing_token"`
}

func (s *ExecutionService) ValidateDocumentAttempt(ctx context.Context, r DocumentAttemptAccess) (map[string]any, error) {
	var a model.ExecutionAttempt
	if err := s.database.WithContext(ctx).First(&a, "id = ?", r.AttemptID).Error; err != nil {
		return nil, mapDatabaseError(err)
	}
	if a.Status != model.StatusRunning || a.WorkerID != r.WorkerID || r.FencingToken == "" || a.FencingToken != r.FencingToken || a.LeaseExpiresAt == nil || !a.LeaseExpiresAt.After(time.Now()) || !a.DeadlineAt.After(time.Now()) || a.TaskID == nil {
		return nil, ErrConflict
	}
	var task model.Task
	if err := s.database.WithContext(ctx).First(&task, "id = ?", *a.TaskID).Error; err != nil {
		return nil, err
	}
	var session model.AgentSession
	if err := s.database.WithContext(ctx).First(&session, "id = ?", a.SessionID).Error; err != nil {
		return nil, err
	}
	if session.ProfileID != "document_ingest_v1" && session.ProfileID != "file_ingest_v1" {
		return nil, ErrForbidden
	}
	if task.Status != model.StatusRunning {
		return nil, ErrConflict
	}
	return map[string]any{"task_id": task.ID, "parent_task_id": task.ParentTaskID, "profile_id": session.ProfileID, "session_id": a.SessionID, "run_id": a.RunID, "input": json.RawMessage(task.Input), "attempt_number": a.Number}, nil
}

func sameDocumentInvocation(input json.RawMessage, id string, generation uint64) bool {
	var value struct {
		DocumentID string `json:"document_id"`
		Generation uint64 `json:"generation"`
	}
	return json.Unmarshal(input, &value) == nil && value.DocumentID == id && value.Generation == generation
}
