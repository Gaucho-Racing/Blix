package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrUnavailable  = errors.New("unavailable")
)

type ExecutionService struct {
	database          *gorm.DB
	workers           *WorkerService
	leaseDuration     time.Duration
	attemptReadyTopic string
	eventTopic        string
	notify            func()
	documents         DocumentScopeValidator
}

func NewExecutionService(db *gorm.DB, workers *WorkerService, cfg config.Config) *ExecutionService {
	return &ExecutionService{database: db, workers: workers, leaseDuration: cfg.ExecutionLeaseDuration, attemptReadyTopic: cfg.MQTTAttemptReadyTopic, eventTopic: cfg.MQTTRunEventTopicPrefix, notify: func() {}}
}
func (s *ExecutionService) SetDispatchNotifier(notify func()) { s.notify = notify }
func mapDatabaseError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}
func pointer(v string) *string { return &v }
func encoded(v any) datatypes.JSON {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func object(b json.RawMessage) bool {
	var v map[string]any
	return len(b) > 0 && json.Unmarshal(b, &v) == nil && v != nil
}
func terminal(status string) bool {
	return status == model.StatusSucceeded || status == model.StatusFailed || status == model.StatusCancelled
}
func (s *ExecutionService) transaction(ctx context.Context, f func(*gorm.DB) error) error {
	err := s.database.WithContext(ctx).Transaction(f)
	if err == nil {
		s.notify()
	}
	return mapDatabaseError(err)
}
func lockRun(tx *gorm.DB, id string) (model.Run, error) {
	var c model.Run
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "id = ?", id).Error
	return c, mapDatabaseError(err)
}
func (s *ExecutionService) event(tx *gorm.DB, a model.ExecutionAttempt, kind, entity string, payload any) (model.Event, error) {
	event := model.Event{ID: identifier.New(identifier.Event), RunID: a.RunID, SessionID: a.SessionID, TurnID: a.TurnID, TaskID: a.TaskID, AttemptID: nil, Type: kind, EntityID: entity, Payload: encoded(payload)}
	if a.ID != "" {
		event.AttemptID = &a.ID
	}
	if err := tx.Create(&event).Error; err != nil {
		return event, err
	}
	if a.WorkerID != "" && kind == "model.response_v1" {
		var usage struct {
			InputTokens  uint64 `json:"input_tokens"`
			OutputTokens uint64 `json:"output_tokens"`
		}
		if err := json.Unmarshal(event.Payload, &usage); err != nil {
			return event, ErrInvalid
		}
		if err := s.workers.recordModelCall(tx, a.WorkerID, usage.InputTokens, usage.OutputTokens); err != nil {
			return event, err
		}
	}

	outbox := model.OutboxMessage{ID: identifier.New(identifier.Outbox), AttemptID: a.ID, Topic: s.eventTopic + "/" + a.RunID + "/events", Payload: encoded(event), AcknowledgeOnPublish: true}
	return event, tx.Create(&outbox).Error
}
func (s *ExecutionService) dispatch(tx *gorm.DB, a model.ExecutionAttempt) error {
	return tx.Create(&model.OutboxMessage{ID: identifier.New(identifier.Outbox), AttemptID: a.ID, Topic: s.attemptReadyTopic + "/" + a.Queue, Payload: encoded(map[string]any{"attempt_id": a.ID, "queue": a.Queue, "runtime_kind": a.RuntimeKind})}).Error
}
func (s *ExecutionService) newAttempt(tx *gorm.DB, runID, sessionID, targetID string, turnID, taskID *string, policy model.ExecutionPolicy, deadline time.Time) (model.ExecutionAttempt, error) {
	var session model.AgentSession
	if err := tx.First(&session, "id = ?", sessionID).Error; err != nil {
		return model.ExecutionAttempt{}, err
	}
	var count int64
	if err := tx.Model(&model.ExecutionAttempt{}).Where("target_id = ?", targetID).Count(&count).Error; err != nil {
		return model.ExecutionAttempt{}, err
	}
	a := model.ExecutionAttempt{ID: identifier.New(identifier.Attempt), RunID: runID, SessionID: sessionID, TargetID: targetID, TurnID: turnID, TaskID: taskID, Number: uint64(count + 1), Queue: policy.Queue, RuntimeKind: session.RuntimeKind, Status: model.StatusQueued, Version: 1, DeadlineAt: deadline}
	if err := tx.Create(&a).Error; err != nil {
		return a, err
	}
	return a, s.dispatch(tx, a)
}
func (s *ExecutionService) createSession(tx *gorm.DB, runID string, parent *string, profile AgentProfile, id string) (model.AgentSession, error) {
	if id == "" {
		id = identifier.New(identifier.Session)
	}
	session := model.AgentSession{ID: id, RunID: runID, ParentSessionID: parent, ProfileID: profile.ID, ProfileVersion: profile.Version, RuntimeKind: profile.RuntimeKind, Status: "ready", Memory: encoded(map[string]any{})}
	if err := tx.Create(&session).Error; err != nil {
		return session, err
	}
	for _, tool := range profile.Tools {
		if err := tx.Create(&model.CapabilityGrant{ID: identifier.New(identifier.Grant), RunID: runID, SessionID: id, ToolID: tool, Scope: encoded(map[string]any{})}).Error; err != nil {
			return session, err
		}
	}
	return session, nil
}

type CreatedRun struct {
	Run     model.Run          `json:"run"`
	Session model.AgentSession `json:"session"`
}

func (s *ExecutionService) CreateRun(ctx context.Context, caller model.CallerContext) (CreatedRun, error) {
	return s.CreateRunWithDocuments(ctx, caller, nil)
}

func (s *ExecutionService) CreateRunWithDocuments(ctx context.Context, caller model.CallerContext, documents []string) (CreatedRun, error) {
	if !identifier.IsValid(caller.EntityID, identifier.Entity) || caller.EntityType != model.EntityTypeUser || !identifier.IsValid(caller.UserID, identifier.User) {
		return CreatedRun{}, ErrForbidden
	}
	if err := validateDocumentIDs(documents); err != nil {
		return CreatedRun{}, err
	}
	if len(documents) > 0 {
		if s.documents == nil {
			return CreatedRun{}, ErrUnavailable
		}
		if err := s.documents.ValidateDocuments(ctx, documents); err != nil {
			return CreatedRun{}, err
		}
	}
	var result CreatedRun
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		c := model.Run{ID: identifier.New(identifier.Run), Kind: model.RunKindChat, CallerEntityID: caller.EntityID, CallerContext: datatypes.NewJSONType(caller), Status: "active", DocumentIDs: datatypes.NewJSONSlice(append([]string{}, documents...))}
		session, err := s.createSession(tx, c.ID, nil, profiles["blix_v1"], "")
		if err != nil {
			return err
		}
		c.LeadSessionID = session.ID
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, model.ExecutionAttempt{RunID: c.ID, SessionID: session.ID}, "run.created_v1", c.ID, c); err != nil {
			return err
		}
		result = CreatedRun{Run: c, Session: session}
		return nil
	})
	return result, err
}

type AppendedMessage struct {
	Message       model.Message `json:"message"`
	Turn          model.Turn    `json:"turn"`
	EventPosition uint64        `json:"event_position"`
}

func (s *ExecutionService) AppendUserMessage(ctx context.Context, runID, owner, content string) (AppendedMessage, error) {
	return s.AppendUserMessageWithDisplay(ctx, runID, owner, content, "")
}

func (s *ExecutionService) AppendUserMessageWithDisplay(ctx context.Context, runID, owner, content, displayContent string) (AppendedMessage, error) {
	content = strings.TrimSpace(content)
	if !identifier.IsValid(runID, identifier.Run) || content == "" || !utf8.ValidString(content) || strings.ContainsRune(content, 0) || len(content) > 1024*1024 {
		return AppendedMessage{}, ErrInvalid
	}
	displayContent = strings.TrimSpace(displayContent)
	if len(displayContent) > 16000 || !utf8.ValidString(displayContent) || strings.ContainsRune(displayContent, 0) {
		return AppendedMessage{}, ErrInvalid
	}
	var result AppendedMessage
	if err := s.validateDocumentChatMessage(ctx, runID, owner); err != nil {
		return result, err
	}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		c, err := lockRun(tx, runID)
		if err != nil {
			return err
		}
		if c.CallerEntityID != owner {
			return ErrForbidden
		}
		var queued int64
		if err := tx.Model(&model.Turn{}).Where("run_id = ? AND status = ?", runID, model.StatusQueued).Count(&queued).Error; err != nil {
			return err
		}
		if queued >= 64 {
			return fmt.Errorf("%w: too many queued turns", ErrConflict)
		}
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", c.LeadSessionID).Error; err != nil {
			return err
		}
		profile, ok := profiles[session.ProfileID]
		if !ok {
			return ErrInvalid
		}
		if session.ProfileID == "document_qa_v1" {
			var turns int64
			if err := tx.Model(&model.Turn{}).Where("run_id = ?", runID).Count(&turns).Error; err != nil {
				return err
			}
			if turns != 0 {
				return fmt.Errorf("%w: document questions require a new run", ErrConflict)
			}
		}
		policy := profile.Policy
		turn := model.Turn{ID: identifier.New(identifier.Turn), RunID: c.ID, SessionID: c.LeadSessionID, Status: model.StatusQueued, Policy: datatypes.NewJSONType(policy), DeadlineAt: time.Now().UTC().Add(time.Duration(policy.DeadlineSeconds) * time.Second), Notifications: encoded([]any{})}
		msg := model.Message{ID: identifier.New(identifier.Message), RunID: runID, TurnID: turn.ID, SessionID: c.LeadSessionID, Role: "user", Content: content, DisplayContent: displayContent}
		e, err := s.event(tx, model.ExecutionAttempt{RunID: runID, SessionID: c.LeadSessionID, TurnID: &turn.ID}, "message.user_v1", msg.ID, map[string]any{"message_id": msg.ID, "content": content, "display_content": displayContent})
		if err != nil {
			return err
		}
		msg.EventPosition = e.Position
		turn.InputPosition = e.Position
		turn.InputMessageID = &msg.ID
		if err := tx.Create(&turn).Error; err != nil {
			return err
		}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		if c.Name == "" {
			name := content
			if displayContent != "" {
				name = displayContent
			}
			runes := []rune(name)
			if len(runes) > 80 {
				runes = runes[:80]
			}
			if err := tx.Model(&c).Update("name", string(runes)).Error; err != nil {
				return err
			}
		}
		if _, err := s.newAttempt(tx, runID, c.LeadSessionID, turn.ID, &turn.ID, nil, policy, turn.DeadlineAt); err != nil {
			return err
		}
		result = AppendedMessage{Message: msg, Turn: turn, EventPosition: e.Position}
		return nil
	})
	return result, err
}

type ClaimRequest struct {
	WorkerID    string `json:"worker_id"`
	RuntimeKind string `json:"runtime_kind"`
}
type AttemptContext struct {
	Attempt           model.ExecutionAttempt   `json:"attempt"`
	Run               model.Run                `json:"run"`
	Session           model.AgentSession       `json:"session"`
	Profile           AgentProfile             `json:"profile"`
	Turn              *model.Turn              `json:"turn,omitempty"`
	Task              *model.Task              `json:"task,omitempty"`
	Checkpoint        *model.RuntimeCheckpoint `json:"checkpoint,omitempty"`
	Messages          []model.Message          `json:"messages"`
	Notifications     json.RawMessage          `json:"notifications"`
	ResumeResult      json.RawMessage          `json:"resume_result,omitempty"`
	Commands          []model.SessionCommand   `json:"commands"`
	AllowedTools      []string                 `json:"allowed_tool_ids"`
	LeaseRenewAfterMS uint64                   `json:"lease_renew_after_ms"`
}

func (s *ExecutionService) ClaimAttempt(ctx context.Context, id string, r ClaimRequest) (AttemptContext, error) {
	if !identifier.IsValid(id, identifier.Attempt) || !identifier.IsValid(r.WorkerID, identifier.Worker) {
		return AttemptContext{}, ErrInvalid
	}
	var result AttemptContext
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var a model.ExecutionAttempt
		if err := tx.First(&a, "id = ?", id).Error; err != nil {
			return err
		}
		c, err := lockRun(tx, a.RunID)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&a, "id = ?", id).Error; err != nil {
			return err
		}
		if a.Status != model.StatusQueued || a.RuntimeKind != r.RuntimeKind || !a.DeadlineAt.After(time.Now()) {
			return ErrConflict
		}
		var active int64
		if err := tx.Model(&model.ExecutionAttempt{}).Where("session_id = ? AND status = ?", a.SessionID, model.StatusRunning).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrConflict
		}
		var earlier int64
		if err := tx.Model(&model.ExecutionAttempt{}).Where("session_id = ? AND status = ? AND created_at < ?", a.SessionID, model.StatusQueued, a.CreatedAt).Count(&earlier).Error; err != nil {
			return err
		}
		if earlier > 0 {
			return ErrConflict
		}
		var worker model.AgentWorker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&worker, "id = ?", r.WorkerID).Error; err != nil {
			return err
		}
		if worker.RuntimeKind != r.RuntimeKind || !worker.LastSeenAt.Add(s.workers.offlineAfter).After(time.Now()) {
			return ErrConflict
		}
		var total, bg int64
		if err := tx.Model(&model.ExecutionAttempt{}).Where("worker_id = ? AND status = ?", worker.ID, model.StatusRunning).Count(&total).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ExecutionAttempt{}).Where("worker_id = ? AND status = ? AND queue = ?", worker.ID, model.StatusRunning, "background").Count(&bg).Error; err != nil {
			return err
		}
		if uint64(total) >= worker.Concurrency || (a.Queue == "background" && uint64(bg) >= worker.BackgroundConcurrency) {
			return ErrConflict
		}
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", a.SessionID).Error; err != nil {
			return err
		}
		profile, ok := profiles[session.ProfileID]
		if !ok {
			return ErrUnavailable
		}
		result = AttemptContext{Run: c, Session: session, Profile: profile, Messages: []model.Message{}, Notifications: json.RawMessage("[]"), Commands: []model.SessionCommand{}, AllowedTools: []string{}, LeaseRenewAfterMS: uint64((s.leaseDuration / 3).Milliseconds())}
		var waiting *string
		if a.TurnID != nil {
			var turn model.Turn
			if err := tx.First(&turn, "id = ?", *a.TurnID).Error; err != nil {
				return err
			}
			if turn.Status != model.StatusQueued {
				return ErrConflict
			}
			waiting = turn.WaitingTaskID
			result.Turn = &turn
			result.Notifications = json.RawMessage(turn.Notifications)
			if err := tx.Table("runway_message AS message").Select("message.*").Joins("JOIN runway_turn origin ON origin.id = message.turn_id").Where("message.run_id = ? AND (message.event_position <= ? OR (message.role = ? AND origin.input_position < ?))", c.ID, turn.InputPosition, "assistant", turn.InputPosition).Order("message.event_position ASC").Find(&result.Messages).Error; err != nil {
				return err
			}
			if err := tx.Model(&turn).Update("status", model.StatusRunning).Error; err != nil {
				return err
			}
		} else {
			var task model.Task
			if err := tx.First(&task, "id = ?", *a.TaskID).Error; err != nil {
				return err
			}
			if task.Status != model.StatusQueued {
				return ErrConflict
			}
			waiting = task.WaitingTaskID
			result.Task = &task
			if err := tx.Model(&task).Update("status", model.StatusRunning).Error; err != nil {
				return err
			}
		}
		if waiting != nil {
			var child model.Task
			if err := tx.First(&child, "id = ?", *waiting).Error; err != nil {
				return err
			}
			if !terminal(child.Status) {
				return ErrConflict
			}
			result.ResumeResult = json.RawMessage(child.Result)
		}
		var cp model.RuntimeCheckpoint
		if err := tx.First(&cp, "target_id = ?", a.TargetID).Error; err == nil {
			result.Checkpoint = &cp
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Where("session_id = ? AND target_id = ? AND acknowledged_at IS NULL", session.ID, a.TargetID).Order("created_at ASC").Find(&result.Commands).Error; err != nil {
			return err
		}
		var grants []model.CapabilityGrant
		if err := tx.Where("session_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", session.ID, time.Now()).Find(&grants).Error; err != nil {
			return err
		}
		for _, g := range grants {
			result.AllowedTools = append(result.AllowedTools, g.ToolID)
		}
		expires := time.Now().UTC().Add(s.leaseDuration)
		a.Status = model.StatusRunning
		a.WorkerID = r.WorkerID
		a.FencingToken = identifier.New(identifier.Attempt)
		a.LeaseExpiresAt = &expires
		a.Version++
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		if err := s.workers.recordClaim(tx, r.WorkerID); err != nil {
			return err
		}
		if err := tx.Model(&model.OutboxMessage{}).Where("attempt_id = ? AND acknowledge_on_publish = false AND acknowledged_at IS NULL", id).Update("acknowledged_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, a, "attempt.claimed_v1", a.ID, map[string]any{"worker_id": r.WorkerID}); err != nil {
			return err
		}
		result.Attempt = a
		return nil
	})
	return result, err
}

type Ownership struct {
	WorkerID        string `json:"worker_id"`
	FencingToken    string `json:"fencing_token"`
	ExpectedVersion uint64 `json:"expected_version"`
}

func (s *ExecutionService) owned(tx *gorm.DB, id string, o Ownership) (model.ExecutionAttempt, error) {
	var a model.ExecutionAttempt
	if err := tx.First(&a, "id = ?", id).Error; err != nil {
		return a, mapDatabaseError(err)
	}
	if _, err := lockRun(tx, a.RunID); err != nil {
		return a, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&a, "id = ?", id).Error; err != nil {
		return a, err
	}
	if a.Status != model.StatusRunning || a.WorkerID != o.WorkerID || a.FencingToken == "" || a.FencingToken != o.FencingToken || a.LeaseExpiresAt == nil || !a.LeaseExpiresAt.After(time.Now()) || !a.DeadlineAt.After(time.Now()) || a.Version != o.ExpectedVersion {
		return a, ErrConflict
	}
	return a, nil
}
func (s *ExecutionService) HeartbeatAttempt(ctx context.Context, id string, o Ownership) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		expires := time.Now().UTC().Add(s.leaseDuration)
		return tx.Model(&a).Update("lease_expires_at", expires).Error
	})
}

type RuntimeEvent struct {
	Type     string          `json:"type"`
	EntityID string          `json:"entity_id"`
	Payload  json.RawMessage `json:"payload"`
}
type CheckpointRequest struct {
	Ownership
	RuntimeKind    string          `json:"runtime_kind"`
	SchemaVersion  uint64          `json:"schema_version"`
	Payload        json.RawMessage `json:"payload"`
	Events         []RuntimeEvent  `json:"events"`
	CommandIDs     []string        `json:"command_ids"`
	Memory         json.RawMessage `json:"memory,omitempty"`
	ResumeConsumed bool            `json:"resume_consumed"`
}

func (s *ExecutionService) checkpoint(tx *gorm.DB, a *model.ExecutionAttempt, r CheckpointRequest) error {
	if r.RuntimeKind != a.RuntimeKind || r.SchemaVersion == 0 || !object(r.Payload) || len(r.Payload) > 8*1024*1024 || len(r.Events) > 100 {
		return ErrInvalid
	}
	cp := model.RuntimeCheckpoint{TargetID: a.TargetID, SessionID: a.SessionID, RuntimeKind: r.RuntimeKind, SchemaVersion: r.SchemaVersion, Payload: datatypes.JSON(r.Payload)}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "target_id"}}, DoUpdates: clause.AssignmentColumns([]string{"runtime_kind", "schema_version", "payload", "updated_at"})}).Create(&cp).Error; err != nil {
		return err
	}
	for _, e := range r.Events {
		if !runtimeEventAllowed(e.Type) || !object(e.Payload) || len(e.Payload) > 1024*1024 || e.EntityID == "" {
			return ErrInvalid
		}
		if _, err := s.event(tx, *a, e.Type, e.EntityID, json.RawMessage(e.Payload)); err != nil {
			return err
		}
	}
	if len(r.Memory) > 0 {
		if !object(r.Memory) {
			return ErrInvalid
		}
		if err := tx.Model(&model.AgentSession{}).Where("id = ?", a.SessionID).Update("memory", datatypes.JSON(r.Memory)).Error; err != nil {
			return err
		}
	}
	if r.ResumeConsumed {
		if err := s.updateTarget(tx, *a, map[string]any{"waiting_task_id": nil}); err != nil {
			return err
		}
	}
	if len(r.CommandIDs) > 0 {
		if err := tx.Model(&model.SessionCommand{}).Where("session_id = ? AND target_id = ? AND id IN ?", a.SessionID, a.TargetID, r.CommandIDs).Update("acknowledged_at", time.Now().UTC()).Error; err != nil {
			return err
		}
	}
	a.Version++
	return tx.Save(a).Error
}
func runtimeEventAllowed(kind string) bool {
	return contains([]string{"model.call_v1", "model.failure_v1", "model.response_v1", "model.output.delta_v1", "caller.verified_v1", "runtime.progress_v1", "runtime.compacted_v1"}, kind)
}
func (s *ExecutionService) Checkpoint(ctx context.Context, id string, r CheckpointRequest) (uint64, error) {
	var version uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		if err := s.checkpoint(tx, &a, r); err != nil {
			return err
		}
		version = a.Version
		return nil
	})
	return version, err
}
func (s *ExecutionService) RecordEvents(ctx context.Context, id string, o Ownership, events []RuntimeEvent) (uint64, error) {
	var v uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		if len(events) > 100 {
			return ErrInvalid
		}
		for _, e := range events {
			if !runtimeEventAllowed(e.Type) || e.EntityID == "" || !object(e.Payload) || len(e.Payload) > 1024*1024 {
				return ErrInvalid
			}
			if _, err := s.event(tx, a, e.Type, e.EntityID, json.RawMessage(e.Payload)); err != nil {
				return err
			}
		}
		a.Version++
		v = a.Version
		return tx.Save(&a).Error
	})
	return v, err
}

func (s *ExecutionService) ListEvents(ctx context.Context, runID string, after uint64) ([]model.Event, error) {
	events := []model.Event{}
	q := s.database.WithContext(ctx).Where("position > ?", after)
	if runID != "" {
		q = q.Where("run_id = ?", runID)
	}
	err := q.Order("position ASC").Limit(100).Find(&events).Error
	return events, err
}
func (s *ExecutionService) ListAllEvents(ctx context.Context, after uint64) ([]model.Event, error) {
	return s.ListEvents(ctx, "", after)
}
func (s *ExecutionService) AuthorizeRunAccess(ctx context.Context, runID, owner string, admin bool) error {
	if !identifier.IsValid(runID, identifier.Run) {
		return ErrInvalid
	}
	var c model.Run
	if err := s.database.WithContext(ctx).First(&c, "id = ?", runID).Error; err != nil {
		return mapDatabaseError(err)
	}
	if !admin && c.CallerEntityID != owner {
		return ErrForbidden
	}
	return nil
}
func (s *ExecutionService) ListRuns(ctx context.Context, owner string, admin bool, kind string) ([]model.Run, error) {
	if kind != "" && kind != model.RunKindChat && kind != model.RunKindInternal {
		return nil, ErrInvalid
	}
	rows := []model.Run{}
	q := s.database.WithContext(ctx)
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if !admin {
		q = q.Where("caller_entity_id = ?", owner)
	}
	err := q.Order("updated_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}
func (s *ExecutionService) ListAllRuns(ctx context.Context) ([]model.Run, error) {
	rows := []model.Run{}
	err := s.database.WithContext(ctx).Order("updated_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}
func (s *ExecutionService) GetRun(ctx context.Context, id string) (map[string]any, error) {
	var c model.Run
	if err := s.database.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return nil, mapDatabaseError(err)
	}
	messages := []model.Message{}
	tasks := []model.Task{}
	turns := []model.Turn{}
	for _, query := range []struct {
		dest  any
		order string
	}{{&messages, "event_position ASC"}, {&tasks, "created_at ASC"}, {&turns, "created_at ASC"}} {
		if err := s.database.WithContext(ctx).Where("run_id = ?", id).Order(query.order).Find(query.dest).Error; err != nil {
			return nil, err
		}
	}
	approvals := []model.Approval{}
	if err := s.database.WithContext(ctx).Where("run_id = ? AND status = ?", id, "pending").Order("created_at ASC").Find(&approvals).Error; err != nil {
		return nil, err
	}
	actions := []model.Action{}
	ids := []string{}
	for _, approval := range approvals {
		ids = append(ids, approval.ActionID)
	}
	if len(ids) > 0 {
		if err := s.database.WithContext(ctx).Where("id IN ?", ids).Find(&actions).Error; err != nil {
			return nil, err
		}
	}
	return map[string]any{"run": c, "messages": messages, "tasks": tasks, "turns": turns, "pending_approvals": approvals, "approval_actions": actions}, nil
}
func (s *ExecutionService) GetTask(ctx context.Context, id string) (model.Task, error) {
	var task model.Task
	err := s.database.WithContext(ctx).First(&task, "id = ?", id).Error
	return task, mapDatabaseError(err)
}
func (s *ExecutionService) ListTasks(ctx context.Context, runID string) ([]model.Task, error) {
	tasks := []model.Task{}
	err := s.database.WithContext(ctx).Where("run_id = ?", runID).Order("created_at DESC").Limit(100).Find(&tasks).Error
	return tasks, err
}
func validateJSON(value json.RawMessage) error {
	if !object(value) {
		return fmt.Errorf("%w: expected an object", ErrInvalid)
	}
	return nil
}

func (s *ExecutionService) UpdateMemory(ctx context.Context, id string, o Ownership, memory json.RawMessage) (uint64, error) {
	if !object(memory) || len(memory) > 65536 {
		return 0, ErrInvalid
	}
	var v uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", a.SessionID).Error; err != nil {
			return err
		}
		var old, new struct {
			ThroughPosition uint64 `json:"through_position"`
		}
		if json.Unmarshal(session.Memory, &old) != nil || json.Unmarshal(memory, &new) != nil {
			return ErrInvalid
		}
		if new.ThroughPosition >= old.ThroughPosition {
			if err := tx.Model(&session).Update("memory", datatypes.JSON(memory)).Error; err != nil {
				return err
			}
		}
		a.Version++
		v = a.Version
		if _, err := s.event(tx, a, "runtime.compacted_v1", a.SessionID, json.RawMessage(memory)); err != nil {
			return err
		}
		return tx.Save(&a).Error
	})
	return v, err
}
