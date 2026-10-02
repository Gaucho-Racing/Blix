package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DelegationRequest struct {
	Ownership
	RequestRef string             `json:"request_ref"`
	Background bool               `json:"background"`
	Invocation json.RawMessage    `json:"invocation"`
	Checkpoint *CheckpointRequest `json:"checkpoint,omitempty"`
}
type invocationEnvelope struct {
	ConversationID  string          `json:"conversation_id"`
	SessionID       string          `json:"session_id"`
	ParentSessionID *string         `json:"parent_session_id"`
	AgentID         string          `json:"agent_id"`
	Objective       string          `json:"objective"`
	Input           json.RawMessage `json:"input"`
}
type DelegatedTask struct {
	TaskID        string `json:"task_id"`
	SessionID     string `json:"session_id"`
	ParentVersion uint64 `json:"parent_version"`
}

func (s *ExecutionService) Delegate(ctx context.Context, id string, r DelegationRequest) (DelegatedTask, error) {
	var invocation invocationEnvelope
	if !object(r.Invocation) || json.Unmarshal(r.Invocation, &invocation) != nil || !identifier.IsValid(invocation.SessionID, identifier.Session) || strings.TrimSpace(invocation.Objective) == "" || !object(invocation.Input) || r.RequestRef == "" || len(r.RequestRef) > 512 {
		return DelegatedTask{}, ErrInvalid
	}
	profile, ok := profiles[invocation.AgentID]
	if !ok || invocation.AgentID == "blix_v1" {
		return DelegatedTask{}, ErrInvalid
	}
	var result DelegatedTask
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		var parentSession model.AgentSession
		if err := tx.First(&parentSession, "id = ?", a.SessionID).Error; err != nil {
			return err
		}
		if !contains(profiles[parentSession.ProfileID].Executors, invocation.AgentID) || invocation.ConversationID != a.ConversationID || invocation.ParentSessionID == nil || *invocation.ParentSessionID != a.SessionID {
			return ErrForbidden
		}
		var existing model.Task
		err = tx.Where("parent_target_id = ? AND delegation_ref = ?", a.TargetID, r.RequestRef).First(&existing).Error
		if err == nil {
			result = DelegatedTask{TaskID: existing.ID, SessionID: existing.SessionID, ParentVersion: a.Version}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var active int64
		if err := tx.Model(&model.Task{}).Where("conversation_id = ? AND status IN ?", a.ConversationID, []string{model.StatusQueued, model.StatusRunning, model.StatusWaiting}).Count(&active).Error; err != nil {
			return err
		}
		if active >= 16 {
			return fmt.Errorf("%w: too many active tasks", ErrConflict)
		}
		childSession, err := s.createSession(tx, a.ConversationID, &a.SessionID, profile, invocation.SessionID)
		if err != nil {
			return err
		}
		task := model.Task{ID: identifier.New(identifier.Task), ConversationID: a.ConversationID, ParentSessionID: a.SessionID, SessionID: childSession.ID, ParentTargetID: a.TargetID, DelegationRef: r.RequestRef, Background: r.Background, Objective: invocation.Objective, Input: datatypes.JSON(invocation.Input), Invocation: datatypes.JSON(r.Invocation), Status: model.StatusQueued, Policy: datatypes.NewJSONType(profile.Policy), DeadlineAt: time.Now().UTC().Add(time.Duration(profile.Policy.DeadlineSeconds) * time.Second)}
		if a.TurnID != nil {
			task.OriginTurnID = a.TurnID
		} else {
			task.ParentTaskID = a.TaskID
			var parent model.Task
			if err := tx.First(&parent, "id = ?", *a.TaskID).Error; err != nil {
				return err
			}
			task.OriginTurnID = parent.OriginTurnID
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		if task.OriginTurnID != nil {
			if err := tx.Create(&model.TurnTask{TurnID: *task.OriginTurnID, TaskID: task.ID, Relation: "origin"}).Error; err != nil {
				return err
			}
		}
		if _, err := s.newAttempt(tx, task.ConversationID, task.SessionID, task.ID, nil, &task.ID, profile.Policy, task.DeadlineAt); err != nil {
			return err
		}
		if _, err := s.event(tx, a, "task.created_v1", childSession.ID, map[string]any{"task_id": task.ID, "session_id": childSession.ID, "agent_id": profile.ID, "objective": task.Objective, "background": r.Background}); err != nil {
			return err
		}
		if !r.Background {
			if r.Checkpoint == nil {
				return ErrInvalid
			}
			if err := s.checkpoint(tx, &a, *r.Checkpoint); err != nil {
				return err
			}
			now := time.Now().UTC()
			a.Status = model.StatusSuspended
			a.CompletedAt = &now
			a.LeaseExpiresAt = nil
			if err := s.updateTarget(tx, a, map[string]any{"status": model.StatusWaiting, "waiting_task_id": task.ID}); err != nil {
				return err
			}
			if _, err := s.event(tx, a, "attempt.suspended_v1", a.ID, map[string]any{"waiting_task_id": task.ID}); err != nil {
				return err
			}
		} else {
			a.Version++
		}
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		result = DelegatedTask{TaskID: task.ID, SessionID: childSession.ID, ParentVersion: a.Version}
		return nil
	})
	return result, err
}
func (s *ExecutionService) updateTarget(tx *gorm.DB, a model.ExecutionAttempt, updates map[string]any) error {
	if a.TurnID != nil {
		return tx.Model(&model.Turn{}).Where("id = ?", *a.TurnID).Updates(updates).Error
	}
	return tx.Model(&model.Task{}).Where("id = ?", *a.TaskID).Updates(updates).Error
}

type CompletionRequest struct {
	Ownership
	Content   string          `json:"content,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Usage     map[string]any  `json:"usage,omitempty"`
	Artifacts []ArtifactInput `json:"artifacts,omitempty"`
}
type ArtifactInput struct {
	Kind       string          `json:"kind"`
	Content    json.RawMessage `json:"content"`
	StorageRef string          `json:"storage_ref,omitempty"`
}

func (s *ExecutionService) Complete(ctx context.Context, id string, r CompletionRequest) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if a.TurnID != nil {
			if strings.TrimSpace(r.Content) == "" || len(r.Content) > 1024*1024 {
				return ErrInvalid
			}
			msg := model.Message{ID: identifier.New(identifier.Message), ConversationID: a.ConversationID, TurnID: *a.TurnID, SessionID: a.SessionID, Role: "assistant", Content: r.Content}
			event, err := s.event(tx, a, "message.assistant_v1", msg.ID, map[string]any{"message_id": msg.ID, "content": msg.Content, "usage": r.Usage})
			if err != nil {
				return err
			}
			msg.EventPosition = event.Position
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
		} else {
			if !object(r.Result) {
				return ErrInvalid
			}
			var result struct {
				ConversationID string          `json:"conversation_id"`
				SessionID      string          `json:"session_id"`
				Status         string          `json:"status"`
				Error          json.RawMessage `json:"error"`
				Output         json.RawMessage `json:"output"`
			}
			if json.Unmarshal(r.Result, &result) != nil || result.ConversationID != a.ConversationID || result.SessionID != a.SessionID || !terminal(result.Status) {
				return ErrInvalid
			}
			if result.Status != model.StatusSucceeded {
				if !object(result.Error) {
					return ErrInvalid
				}
				return s.failOwned(tx, &a, result.Error, result.Status)
			}
			if err := s.updateTarget(tx, a, map[string]any{"result": datatypes.JSON(r.Result)}); err != nil {
				return err
			}
			if len(r.Artifacts) == 0 {
				r.Artifacts = []ArtifactInput{{Kind: "agent_result", Content: r.Result}}
			}
		}
		if len(r.Artifacts) > 20 {
			return ErrInvalid
		}
		for _, input := range r.Artifacts {
			if input.Kind == "" || !json.Valid(input.Content) || len(input.Content) > 4*1024*1024 {
				return ErrInvalid
			}
			artifact := model.Artifact{ID: identifier.New(identifier.Artifact), ConversationID: a.ConversationID, TaskID: a.TaskID, AttemptID: a.ID, Kind: input.Kind, Content: datatypes.JSON(input.Content), StorageRef: input.StorageRef, Metadata: encoded(map[string]any{})}
			if err := tx.Create(&artifact).Error; err != nil {
				return err
			}
			if _, err := s.event(tx, a, "artifact.created_v1", artifact.ID, artifact); err != nil {
				return err
			}
		}
		a.Status = model.StatusSucceeded
		a.CompletedAt = &now
		a.LeaseExpiresAt = nil
		a.Version++
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		if err := s.updateTarget(tx, a, map[string]any{"status": model.StatusSucceeded, "completed_at": now, "waiting_task_id": nil}); err != nil {
			return err
		}
		if err := s.workers.recordCompletion(tx, a.WorkerID, model.StatusSucceeded); err != nil {
			return err
		}
		if a.TaskID != nil {
			return s.taskSettled(tx, *a.TaskID)
		}
		return nil
	})
}
func (s *ExecutionService) taskSettled(tx *gorm.DB, id string) error {
	var task model.Task
	if err := tx.First(&task, "id = ?", id).Error; err != nil {
		return err
	}
	var session model.AgentSession
	if err := tx.First(&session, "id = ?", task.SessionID).Error; err != nil {
		return err
	}
	eventAttempt := model.ExecutionAttempt{ConversationID: task.ConversationID, SessionID: task.ParentSessionID, TaskID: &task.ID}
	if _, err := s.event(tx, eventAttempt, "task.result_v1", task.SessionID, map[string]any{"task_id": task.ID, "agent_id": session.ProfileID, "status": task.Status, "result": json.RawMessage(task.Result)}); err != nil {
		return err
	}
	if task.Background {
		var conversation model.Conversation
		if err := tx.First(&conversation, "id = ?", task.ConversationID).Error; err != nil {
			return err
		}
		policy := profiles["blix_v1"].Policy
		var position uint64
		if err := tx.Model(&model.Event{}).Where("conversation_id = ?", conversation.ID).Select("COALESCE(MAX(position),0)").Scan(&position).Error; err != nil {
			return err
		}
		turn := model.Turn{ID: identifier.New(identifier.Turn), ConversationID: conversation.ID, SessionID: conversation.LeadSessionID, TriggerTaskID: &task.ID, InputPosition: position, Status: model.StatusQueued, Policy: datatypes.NewJSONType(policy), DeadlineAt: time.Now().UTC().Add(time.Duration(policy.DeadlineSeconds) * time.Second), Notifications: encoded([]any{map[string]any{"task_id": task.ID, "session_id": task.SessionID, "status": task.Status, "result": json.RawMessage(task.Result)}})}
		if err := tx.Create(&turn).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.TurnTask{TurnID: turn.ID, TaskID: task.ID, Relation: "completion"}).Error; err != nil {
			return err
		}
		_, err := s.newAttempt(tx, turn.ConversationID, turn.SessionID, turn.ID, &turn.ID, nil, policy, turn.DeadlineAt)
		return err
	}
	if identifier.IsValid(task.ParentTargetID, identifier.Turn) {
		var turn model.Turn
		if err := tx.First(&turn, "id = ?", task.ParentTargetID).Error; err != nil {
			return err
		}
		if turn.Status != model.StatusWaiting || turn.WaitingTaskID == nil || *turn.WaitingTaskID != task.ID {
			return nil
		}
		if err := tx.Model(&turn).Update("status", model.StatusQueued).Error; err != nil {
			return err
		}
		_, err := s.newAttempt(tx, turn.ConversationID, turn.SessionID, turn.ID, &turn.ID, nil, turn.Policy.Data(), turn.DeadlineAt)
		return err
	}
	var parent model.Task
	if err := tx.First(&parent, "id = ?", task.ParentTargetID).Error; err != nil {
		return err
	}
	if parent.Status != model.StatusWaiting || parent.WaitingTaskID == nil || *parent.WaitingTaskID != task.ID {
		return nil
	}
	if err := tx.Model(&parent).Update("status", model.StatusQueued).Error; err != nil {
		return err
	}
	_, err := s.newAttempt(tx, parent.ConversationID, parent.SessionID, parent.ID, nil, &parent.ID, parent.Policy.Data(), parent.DeadlineAt)
	return err
}

type FailureRequest struct {
	Ownership
	Error     json.RawMessage `json:"error"`
	Retryable bool            `json:"retryable"`
}

func (s *ExecutionService) Fail(ctx context.Context, id string, r FailureRequest) error {
	if !object(r.Error) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		if r.Retryable {
			return s.retryOwned(tx, &a, r.Error)
		}
		return s.failOwned(tx, &a, r.Error, model.StatusFailed)
	})
}
func (s *ExecutionService) failOwned(tx *gorm.DB, a *model.ExecutionAttempt, failure json.RawMessage, status string) error {
	now := time.Now().UTC()
	a.Status = status
	a.CompletedAt = &now
	a.LeaseExpiresAt = nil
	a.Error = datatypes.JSON(failure)
	a.Version++
	if err := tx.Save(a).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.OutboxMessage{}).Where("attempt_id = ? AND acknowledge_on_publish = false AND acknowledged_at IS NULL", a.ID).Update("acknowledged_at", now).Error; err != nil {
		return err
	}
	updates := map[string]any{"status": status, "completed_at": now}
	if a.TaskID != nil {
		updates["error"] = datatypes.JSON(failure)
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", a.SessionID).Error; err != nil {
			return err
		}
		updates["result"] = encoded(map[string]any{"schema_version": 1, "conversation_id": a.ConversationID, "session_id": a.SessionID, "parent_session_id": session.ParentSessionID, "agent_id": session.ProfileID, "status": status, "error": json.RawMessage(failure), "usage": map[string]any{}})
	}
	if err := s.updateTarget(tx, *a, updates); err != nil {
		return err
	}
	if _, err := s.event(tx, *a, "attempt.failed_v1", a.ID, map[string]any{"error": json.RawMessage(failure)}); err != nil {
		return err
	}
	if a.WorkerID != "" {
		if err := s.workers.recordCompletion(tx, a.WorkerID, status); err != nil {
			return err
		}
	}
	if a.TaskID != nil {
		return s.taskSettled(tx, *a.TaskID)
	}
	return nil
}
func (s *ExecutionService) targetPolicy(tx *gorm.DB, a model.ExecutionAttempt) (model.ExecutionPolicy, error) {
	if a.TurnID != nil {
		var turn model.Turn
		err := tx.First(&turn, "id = ?", *a.TurnID).Error
		return turn.Policy.Data(), err
	}
	var task model.Task
	err := tx.First(&task, "id = ?", *a.TaskID).Error
	return task.Policy.Data(), err
}
func (s *ExecutionService) retryOwned(tx *gorm.DB, a *model.ExecutionAttempt, failure json.RawMessage) error {
	policy, err := s.targetPolicy(tx, *a)
	if err != nil {
		return err
	}
	var failed int64
	if err := tx.Model(&model.ExecutionAttempt{}).Where("target_id = ? AND status IN ?", a.TargetID, []string{model.StatusExpired, model.StatusFailed}).Count(&failed).Error; err != nil {
		return err
	}
	if uint64(failed+1) >= policy.MaxAttempts || !a.DeadlineAt.After(time.Now()) {
		return s.failOwned(tx, a, failure, model.StatusFailed)
	}
	now := time.Now().UTC()
	a.Status = model.StatusExpired
	a.CompletedAt = &now
	a.LeaseExpiresAt = nil
	a.Error = datatypes.JSON(failure)
	a.Version++
	if err := tx.Save(a).Error; err != nil {
		return err
	}
	if err := s.updateTarget(tx, *a, map[string]any{"status": model.StatusQueued}); err != nil {
		return err
	}
	if _, err := s.event(tx, *a, "attempt.lease_expired_v1", a.ID, map[string]any{"error": json.RawMessage(failure)}); err != nil {
		return err
	}
	_, err = s.newAttempt(tx, a.ConversationID, a.SessionID, a.TargetID, a.TurnID, a.TaskID, policy, a.DeadlineAt)
	return err
}
func (s *ExecutionService) RecoverExpired(ctx context.Context) error {
	var candidates []model.ExecutionAttempt
	now := time.Now().UTC()
	if err := s.database.WithContext(ctx).Where("(status = ? AND lease_expires_at <= ?) OR (status IN ? AND deadline_at <= ?)", model.StatusRunning, now, []string{model.StatusQueued, model.StatusRunning, model.StatusSuspended}, now).Where("NOT EXISTS (SELECT 1 FROM runway_execution_attempt newer WHERE newer.target_id = runway_execution_attempt.target_id AND newer.number > runway_execution_attempt.number)").Order("created_at ASC").Limit(100).Find(&candidates).Error; err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := s.transaction(ctx, func(tx *gorm.DB) error {
			if _, err := lockConversation(tx, candidate.ConversationID); err != nil {
				return err
			}
			var a model.ExecutionAttempt
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&a, "id = ?", candidate.ID).Error; err != nil {
				return err
			}
			if a.Status != candidate.Status {
				return nil
			}
			if a.TurnID != nil {
				var turn model.Turn
				if err := tx.First(&turn, "id = ?", *a.TurnID).Error; err != nil {
					return err
				}
				if terminal(turn.Status) {
					return nil
				}
			} else {
				var task model.Task
				if err := tx.First(&task, "id = ?", *a.TaskID).Error; err != nil {
					return err
				}
				if terminal(task.Status) {
					return nil
				}
			}

			failure := encoded(map[string]any{"code": "lease_expired", "message": "The runtime lost its lease.", "retryable": true})
			if !a.DeadlineAt.After(time.Now()) {
				failure = encoded(map[string]any{"code": "deadline_exceeded", "message": "The execution deadline was exceeded.", "retryable": false})
				return s.failOwned(tx, &a, json.RawMessage(failure), model.StatusFailed)
			}
			if a.Status == model.StatusRunning && a.LeaseExpiresAt != nil && !a.LeaseExpiresAt.After(time.Now()) {
				return s.retryOwned(tx, &a, json.RawMessage(failure))
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func (s *ExecutionService) InterruptConversation(ctx context.Context, cid, owner string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		c, err := lockConversation(tx, cid)
		if err != nil {
			return err
		}
		if c.CallerEntityID != owner {
			return ErrForbidden
		}
		var turns []model.Turn
		if err := tx.Where("conversation_id = ? AND status IN ?", cid, []string{model.StatusQueued, model.StatusRunning, model.StatusWaiting}).Find(&turns).Error; err != nil {
			return err
		}
		for _, turn := range turns {
			if err := s.cancelTarget(tx, cid, turn.SessionID, turn.ID, true); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *ExecutionService) cancelTarget(tx *gorm.DB, cid, sessionID, targetID string, turn bool) error {
	now := time.Now().UTC()
	a := model.ExecutionAttempt{ConversationID: cid, SessionID: sessionID, TargetID: targetID}
	if turn {
		a.TurnID = &targetID
	} else {
		a.TaskID = &targetID
	}
	if err := s.updateTarget(tx, a, map[string]any{"status": model.StatusCancelled, "completed_at": now}); err != nil {
		return err
	}
	if err := tx.Model(&model.ExecutionAttempt{}).Where("target_id = ? AND status IN ?", targetID, []string{model.StatusQueued, model.StatusRunning, model.StatusSuspended}).Updates(map[string]any{"status": model.StatusCancelled, "completed_at": now, "lease_expires_at": nil}).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.OutboxMessage{}).Where("attempt_id IN (SELECT id FROM runway_execution_attempt WHERE target_id = ?) AND acknowledge_on_publish = false AND acknowledged_at IS NULL", targetID).Update("acknowledged_at", now).Error; err != nil {
		return err
	}
	if err := tx.Create(&model.SessionCommand{ID: identifier.New(identifier.Command), SessionID: sessionID, TargetID: targetID, Kind: "cancel", Payload: encoded(map[string]any{})}).Error; err != nil {
		return err
	}
	if _, err := s.event(tx, a, "attempt.cancelled_v1", targetID, map[string]any{}); err != nil {
		return err
	}
	var children []model.Task
	if err := tx.Where("parent_target_id = ? AND background = ? AND status IN ?", targetID, false, []string{model.StatusQueued, model.StatusRunning, model.StatusWaiting}).Find(&children).Error; err != nil {
		return err
	}
	for _, child := range children {
		if err := s.cancelTarget(tx, cid, child.SessionID, child.ID, false); err != nil {
			return err
		}
	}
	if !turn {
		var task model.Task
		var session model.AgentSession
		if err := tx.First(&task, "id = ?", targetID).Error; err != nil {
			return err
		}
		if err := tx.First(&session, "id = ?", sessionID).Error; err != nil {
			return err
		}
		result := encoded(map[string]any{"schema_version": 1, "conversation_id": cid, "session_id": sessionID, "parent_session_id": session.ParentSessionID, "agent_id": session.ProfileID, "status": "cancelled", "usage": map[string]any{}, "error": map[string]any{"code": "task_cancelled", "message": "The user cancelled this task."}})
		if err := tx.Model(&task).Update("result", result).Error; err != nil {
			return err
		}
		return s.taskSettled(tx, targetID)
	}
	return nil
}
func (s *ExecutionService) ControlTask(ctx context.Context, id, owner, operation, content string) error {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		c, err := lockConversation(tx, task.ConversationID)
		if err != nil {
			return err
		}
		if c.CallerEntityID != owner {
			return ErrForbidden
		}
		if err := tx.First(&task, "id = ?", id).Error; err != nil {
			return err
		}
		return s.controlTask(tx, task, operation, content, "")
	})
}
func (s *ExecutionService) Commands(ctx context.Context, id string, o Ownership) ([]model.SessionCommand, error) {
	commands := []model.SessionCommand{}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		return tx.Where("session_id = ? AND target_id = ? AND acknowledged_at IS NULL", a.SessionID, a.TargetID).Order("created_at ASC").Find(&commands).Error
	})
	return commands, err
}

func (s *ExecutionService) controlTask(tx *gorm.DB, task model.Task, operation, content, ref string) error {
	if operation == "cancel" && task.Status == model.StatusCancelled {
		return nil
	}
	if ref != "" {
		var count int64
		if err := tx.Model(&model.SessionCommand{}).Where("target_id = ? AND kind = ? AND request_ref = ?", task.ID, operation, ref).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	if terminal(task.Status) {
		return ErrConflict
	}
	if operation == "cancel" {
		return s.cancelTarget(tx, task.ConversationID, task.SessionID, task.ID, false)
	}
	if operation != "steer" || strings.TrimSpace(content) == "" || len(content) > 65536 {
		return ErrInvalid
	}
	command := model.SessionCommand{RequestRef: ref, ID: identifier.New(identifier.Command), SessionID: task.SessionID, TargetID: task.ID, Kind: "steer", Payload: encoded(map[string]any{"content": content})}
	if err := tx.Create(&command).Error; err != nil {
		return err
	}
	_, err := s.event(tx, model.ExecutionAttempt{ConversationID: task.ConversationID, SessionID: task.SessionID, TaskID: &task.ID}, "task.steered_v1", task.ID, map[string]any{"content": content})
	return err
}

type TaskControlRequest struct {
	RequestRef string `json:"request_ref"`
	Ownership
	Operation string `json:"operation"`
	TaskID    string `json:"task_id"`
	Content   string `json:"content"`
}

func (s *ExecutionService) SessionTaskControl(ctx context.Context, id string, r TaskControlRequest) (map[string]any, error) {
	result := map[string]any{}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		var grant model.CapabilityGrant
		if err := tx.Where("session_id = ? AND tool_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", a.SessionID, "agent_execution_v1", time.Now()).First(&grant).Error; err != nil {
			return ErrForbidden
		}
		if r.Operation == "list" {
			tasks := []model.Task{}
			if err := tx.Where("conversation_id = ? AND parent_session_id = ?", a.ConversationID, a.SessionID).Order("created_at DESC").Limit(100).Find(&tasks).Error; err != nil {
				return err
			}
			result["tasks"] = tasks
			return nil
		}
		var task model.Task
		if err := tx.First(&task, "id = ?", r.TaskID).Error; err != nil {
			return err
		}
		if task.ConversationID != a.ConversationID || task.ParentSessionID != a.SessionID {
			return ErrForbidden
		}
		if r.Operation == "status" {
			result["status"] = task.Status
			result["result"] = json.RawMessage(task.Result)
			return nil
		}
		if err := s.controlTask(tx, task, r.Operation, r.Content, r.RequestRef); err != nil {
			return err
		}
		a.Version++
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		result["version"] = a.Version
		result["task_id"] = task.ID
		result["operation"] = r.Operation
		return nil
	})
	return result, err
}
