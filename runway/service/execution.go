package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound     = errors.New("resource not found")
	ErrConflict     = errors.New("resource state conflict")
	ErrInvalid      = errors.New("invalid request")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrUnavailable  = errors.New("service unavailable")
)

var versionedIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*_v[1-9][0-9]*$`)

type ExecutionService struct {
	database            *gorm.DB
	workers             *WorkerService
	executionReadyTopic string
	runEventTopicPrefix string
	leaseDuration       time.Duration
}

type CreatedRun struct {
	Run       model.Run            `json:"run"`
	Execution model.AgentExecution `json:"execution"`
}

type AppendedMessage struct {
	Message       model.Message `json:"message"`
	EventPosition uint64        `json:"event_position"`
	Version       uint64        `json:"version"`
}

type ExecutionContext struct {
	RunID                string              `json:"run_id"`
	CallerEntityID       string              `json:"caller_entity_id"`
	CallerContext        model.CallerContext `json:"caller_context"`
	ExecutionID          string              `json:"execution_id"`
	ParentExecutionID    *string             `json:"parent_execution_id"`
	AgentID              string              `json:"agent_id"`
	Version              uint64              `json:"version"`
	Task                 json.RawMessage     `json:"task,omitempty"`
	State                json.RawMessage     `json:"state,omitempty"`
	WaitingOnExecutionID *string             `json:"waiting_on_execution_id,omitempty"`
	LeaseExpiresAt       *time.Time          `json:"lease_expires_at"`
	LeaseRenewAfterMS    uint64              `json:"lease_renew_after_ms"`
	Messages             []model.Message     `json:"messages"`
}

type Heartbeat struct {
	WorkerID string `json:"worker_id"`
}

type LeaseHeartbeat struct {
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type Delegation struct {
	WorkerID        string          `json:"worker_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Task            json.RawMessage `json:"task"`
	State           json.RawMessage `json:"state"`
}

type DelegatedExecution struct {
	ExecutionID   string `json:"execution_id"`
	ParentVersion uint64 `json:"parent_version"`
}

type ExecutionResult struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

type ToolCheckpoint struct {
	WorkerID        string          `json:"worker_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Call            json.RawMessage `json:"call"`
	Result          json.RawMessage `json:"result"`
	State           json.RawMessage `json:"state"`
}

type ModelCheckpoint struct {
	WorkerID        string          `json:"worker_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Model           string          `json:"model"`
	TurnMessageID   string          `json:"turn_message_id"`
	Sequence        uint64          `json:"sequence"`
	InputTokens     uint64          `json:"input_tokens"`
	OutputTokens    uint64          `json:"output_tokens"`
	CallableIDs     []string        `json:"callable_ids"`
	HasContent      bool            `json:"has_content"`
	Response        json.RawMessage `json:"response"`
	State           json.RawMessage `json:"state"`
}

type toolCallPayload struct {
	RunID             string          `json:"run_id"`
	CallID            string          `json:"call_id"`
	ParentExecutionID string          `json:"parent_execution_id"`
	ToolID            string          `json:"tool_id"`
	Arguments         json.RawMessage `json:"arguments"`
}

type toolResultPayload struct {
	RunID             string          `json:"run_id"`
	CallID            string          `json:"call_id"`
	ParentExecutionID string          `json:"parent_execution_id"`
	Status            string          `json:"status"`
	Output            json.RawMessage `json:"output"`
	Error             json.RawMessage `json:"error"`
}

type Completion struct {
	WorkerID        string         `json:"worker_id"`
	ExpectedVersion uint64         `json:"expected_version"`
	Content         string         `json:"content"`
	Usage           map[string]any `json:"usage"`
}

type AgentCompletion struct {
	WorkerID        string          `json:"worker_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Result          json.RawMessage `json:"result"`
}

type Failure struct {
	WorkerID        string         `json:"worker_id"`
	ExpectedVersion uint64         `json:"expected_version"`
	Error           map[string]any `json:"error"`
}

type agentTaskPayload struct {
	SchemaVersion     uint64          `json:"schema_version"`
	RunID             string          `json:"run_id"`
	ExecutionID       string          `json:"execution_id"`
	ParentExecutionID *string         `json:"parent_execution_id"`
	AgentID           string          `json:"agent_id"`
	Objective         string          `json:"objective"`
	Input             json.RawMessage `json:"input"`
}

type pendingAgentCallPayload struct {
	RequestRef   string           `json:"request_ref"`
	RequestIndex uint64           `json:"request_index"`
	Task         agentTaskPayload `json:"task"`
}

type agentExecutionStatePayload struct {
	SchemaVersion    uint64                   `json:"schema_version"`
	AgentID          string                   `json:"agent_id"`
	Phase            string                   `json:"phase"`
	NextCallIndex    uint64                   `json:"next_call_index"`
	PendingAgentCall *pendingAgentCallPayload `json:"pending_agent_call"`
}

type agentResultPayload struct {
	RunID             string          `json:"run_id"`
	ExecutionID       string          `json:"execution_id"`
	ParentExecutionID *string         `json:"parent_execution_id"`
	AgentID           string          `json:"agent_id"`
	Status            string          `json:"status"`
	Output            json.RawMessage `json:"output"`
	Error             json.RawMessage `json:"error"`
}

func NewExecutionService(database *gorm.DB, workers *WorkerService, configuration config.Config) *ExecutionService {
	return &ExecutionService{
		database:            database,
		workers:             workers,
		executionReadyTopic: configuration.MQTTExecutionReadyTopic,
		runEventTopicPrefix: configuration.MQTTRunEventTopicPrefix,
		leaseDuration:       configuration.ExecutionLeaseDuration,
	}
}

func (s *ExecutionService) CreateRun(ctx context.Context, agentID string, callerContext model.CallerContext) (CreatedRun, error) {
	agentID = strings.TrimSpace(agentID)
	if !versionedIdentifierPattern.MatchString(agentID) {
		return CreatedRun{}, fmt.Errorf("%w: agent_id must be a versioned identifier", ErrInvalid)
	}
	if err := validateRunCallerContext(callerContext); err != nil {
		return CreatedRun{}, err
	}
	run := model.Run{
		ID:             identifier.New(identifier.Run),
		CallerEntityID: callerContext.EntityID,
		CallerContext:  datatypes.NewJSONType(callerContext),
		Kind:           model.RunKindConversation,
		Status:         model.RunStatusActive,
	}
	execution := model.AgentExecution{
		ID:      identifier.New(identifier.Execution),
		RunID:   run.ID,
		AgentID: agentID,
		Status:  model.ExecutionStatusIdle,
		Version: 1,
	}
	run.RootExecutionID = execution.ID
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if err := tx.Create(&execution).Error; err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"run_id":            run.ID,
			"root_execution_id": execution.ID,
			"agent_id":          execution.AgentID,
			"caller_entity_id":  run.CallerEntityID,
			"caller_context":    callerContext,
		})
		if err != nil {
			return err
		}
		return s.appendEvent(tx, run.ID, execution.ID, model.EventTypeRunCreated, run.ID, payload)
	})
	if err != nil {
		return CreatedRun{}, fmt.Errorf("create run: %w", err)
	}
	return CreatedRun{Run: run, Execution: execution}, nil
}

func validateRunCallerContext(caller model.CallerContext) error {
	if !identifier.IsValid(caller.EntityID, identifier.Entity) {
		return fmt.Errorf("%w: caller context entity_id is invalid", ErrInvalid)
	}
	switch caller.EntityType {
	case model.EntityTypeUser:
		if !identifier.IsValid(caller.UserID, identifier.User) || caller.ServiceAccountID != "" {
			return fmt.Errorf("%w: caller context user identity is invalid", ErrInvalid)
		}
	case model.EntityTypeServiceAccount:
		if !identifier.IsValid(caller.ServiceAccountID, identifier.ServiceAccount) || caller.UserID != "" {
			return fmt.Errorf("%w: caller context service-account identity is invalid", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: caller context entity_type is invalid", ErrInvalid)
	}
	return nil
}

func (s *ExecutionService) AppendUserMessage(ctx context.Context, runID string, callerEntityID string, content string) (AppendedMessage, error) {
	if !identifier.IsValid(runID, identifier.Run) {
		return AppendedMessage{}, fmt.Errorf("%w: run_id is invalid", ErrInvalid)
	}
	callerEntityID = strings.TrimSpace(callerEntityID)
	if !identifier.IsValid(callerEntityID, identifier.Entity) {
		return AppendedMessage{}, fmt.Errorf("%w: caller entity_id is invalid", ErrInvalid)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return AppendedMessage{}, fmt.Errorf("%w: content cannot be empty", ErrInvalid)
	}
	var appended AppendedMessage
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.Run
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id = ?", runID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if run.CallerEntityID != callerEntityID {
			return ErrForbidden
		}
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", run.RootExecutionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusIdle {
			return ErrConflict
		}

		message := model.Message{
			ID:                identifier.New(identifier.Message),
			RunID:             run.ID,
			ParentExecutionID: execution.ID,
			Role:              model.MessageRoleUser,
			Content:           content,
		}
		payload, err := json.Marshal(map[string]any{
			"message_id": message.ID,
			"role":       message.Role,
			"content":    message.Content,
		})
		if err != nil {
			return err
		}
		event := model.ExecutionEvent{
			ID:          identifier.New(identifier.Event),
			RunID:       run.ID,
			ExecutionID: execution.ID,
			Type:        model.EventTypeUserMessage,
			EntityID:    message.ID,
			Payload:     payload,
		}
		if err := s.persistEvent(tx, &event); err != nil {
			return err
		}
		message.EventPosition = event.Position
		if err := tx.Create(&message).Error; err != nil {
			return err
		}

		execution.Status = model.ExecutionStatusQueued
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		execution.LeaseExpiresAt = nil
		execution.State = nil
		execution.WaitingOnExecutionID = nil
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		if err := s.createDispatch(tx, execution); err != nil {
			return err
		}
		appended = AppendedMessage{
			Message:       message,
			EventPosition: event.Position,
			Version:       execution.Version,
		}
		return nil
	})
	if err != nil {
		return AppendedMessage{}, err
	}
	return appended, nil
}

func (s *ExecutionService) ClaimExecution(ctx context.Context, executionID string, workerID string) (ExecutionContext, error) {
	workerID = strings.TrimSpace(workerID)
	if !identifier.IsValid(executionID, identifier.Execution) || !identifier.IsValid(workerID, identifier.Worker) {
		return ExecutionContext{}, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	var claimed ExecutionContext
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusQueued {
			return ErrConflict
		}
		if err := s.workers.recordClaim(tx, workerID); err != nil {
			return err
		}
		now := time.Now().UTC()
		leaseExpiresAt := now.Add(s.leaseDuration)
		execution.Status = model.ExecutionStatusRunning
		execution.ClaimedBy = workerID
		execution.ClaimedAt = &now
		execution.LeaseExpiresAt = &leaseExpiresAt
		execution.Version++
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		claimedPayload, err := json.Marshal(map[string]any{
			"worker_id":        workerID,
			"lease_expires_at": leaseExpiresAt,
			"version":          execution.Version,
		})
		if err != nil {
			return err
		}
		if err := s.appendEvent(
			tx,
			execution.RunID,
			execution.ID,
			model.EventTypeExecutionClaimed,
			execution.ID,
			claimedPayload,
		); err != nil {
			return err
		}
		if err := tx.Model(&model.OutboxMessage{}).
			Where("execution_id = ? AND acknowledged_at IS NULL", execution.ID).
			Update("acknowledged_at", now).Error; err != nil {
			return err
		}
		var messages []model.Message
		if err := tx.Where("parent_execution_id = ?", execution.ID).Order("event_position ASC").Find(&messages).Error; err != nil {
			return err
		}
		var run model.Run
		if err := tx.Select("caller_entity_id", "caller_context").First(&run, "id = ?", execution.RunID).Error; err != nil {
			return mapDatabaseError(err)
		}
		claimed = ExecutionContext{
			RunID:                execution.RunID,
			CallerEntityID:       run.CallerEntityID,
			CallerContext:        run.CallerContext.Data(),
			ExecutionID:          execution.ID,
			ParentExecutionID:    execution.ParentExecutionID,
			AgentID:              execution.AgentID,
			Version:              execution.Version,
			Task:                 json.RawMessage(execution.Task),
			State:                json.RawMessage(execution.State),
			WaitingOnExecutionID: execution.WaitingOnExecutionID,
			LeaseExpiresAt:       execution.LeaseExpiresAt,
			LeaseRenewAfterMS:    uint64((s.leaseDuration / 3).Milliseconds()),
			Messages:             messages,
		}
		return nil
	})
	if err != nil {
		return ExecutionContext{}, err
	}
	return claimed, nil
}

func (s *ExecutionService) HeartbeatExecution(
	ctx context.Context,
	executionID string,
	heartbeat Heartbeat,
) (LeaseHeartbeat, error) {
	workerID := strings.TrimSpace(heartbeat.WorkerID)
	if !identifier.IsValid(executionID, identifier.Execution) || !identifier.IsValid(workerID, identifier.Worker) {
		return LeaseHeartbeat{}, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	heartbeatAt := time.Now().UTC()
	leaseExpiresAt := heartbeatAt.Add(s.leaseDuration)
	result := s.database.WithContext(ctx).
		Model(&model.AgentExecution{}).
		Where(
			"id = ? AND status = ? AND claimed_by = ? AND lease_expires_at > ?",
			executionID,
			model.ExecutionStatusRunning,
			workerID,
			heartbeatAt,
		).
		Update("lease_expires_at", leaseExpiresAt)
	if result.Error != nil {
		return LeaseHeartbeat{}, result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.database.WithContext(ctx).Model(&model.AgentExecution{}).Where("id = ?", executionID).Count(&count).Error; err != nil {
			return LeaseHeartbeat{}, err
		}
		if count == 0 {
			return LeaseHeartbeat{}, ErrNotFound
		}
		return LeaseHeartbeat{}, ErrConflict
	}
	return LeaseHeartbeat{LeaseExpiresAt: leaseExpiresAt}, nil
}

func (s *ExecutionService) DelegateExecution(
	ctx context.Context,
	parentExecutionID string,
	delegation Delegation,
) (DelegatedExecution, error) {
	if !identifier.IsValid(parentExecutionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(delegation.WorkerID), identifier.Worker) {
		return DelegatedExecution{}, fmt.Errorf("%w: parent_execution_id or worker_id is invalid", ErrInvalid)
	}
	var task agentTaskPayload
	if err := json.Unmarshal(delegation.Task, &task); err != nil {
		return DelegatedExecution{}, fmt.Errorf("%w: task is invalid: %v", ErrInvalid, err)
	}
	state, err := decodeExecutionState(delegation.State)
	if err != nil {
		return DelegatedExecution{}, err
	}
	if task.SchemaVersion != 1 ||
		!identifier.IsValid(task.RunID, identifier.Run) ||
		!identifier.IsValid(task.ExecutionID, identifier.Execution) ||
		task.ExecutionID == parentExecutionID {
		return DelegatedExecution{}, fmt.Errorf("%w: child execution_id is invalid", ErrInvalid)
	}
	if task.ParentExecutionID == nil || *task.ParentExecutionID != parentExecutionID {
		return DelegatedExecution{}, fmt.Errorf("%w: task parent_execution_id is invalid", ErrInvalid)
	}
	if !versionedIdentifierPattern.MatchString(task.AgentID) {
		return DelegatedExecution{}, fmt.Errorf("%w: task agent_id is invalid", ErrInvalid)
	}
	if strings.TrimSpace(task.Objective) == "" || !validJSONObject(task.Input) {
		return DelegatedExecution{}, fmt.Errorf("%w: task objective and object input are required", ErrInvalid)
	}
	if state.Phase != "waiting_for_agent" ||
		state.PendingAgentCall == nil ||
		state.PendingAgentCall.Task.ExecutionID != task.ExecutionID ||
		state.PendingAgentCall.Task.RunID != task.RunID ||
		state.PendingAgentCall.Task.AgentID != task.AgentID ||
		state.PendingAgentCall.Task.ParentExecutionID == nil ||
		*state.PendingAgentCall.Task.ParentExecutionID != parentExecutionID {
		return DelegatedExecution{}, fmt.Errorf("%w: state does not describe the delegated child", ErrInvalid)
	}

	var delegated DelegatedExecution
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parent, err := s.lockOwnedExecution(
			tx,
			parentExecutionID,
			delegation.WorkerID,
			delegation.ExpectedVersion,
		)
		if err != nil {
			return err
		}
		if task.RunID != parent.RunID {
			return ErrConflict
		}
		if state.AgentID != parent.AgentID {
			return ErrConflict
		}

		child := model.AgentExecution{
			ID:                task.ExecutionID,
			RunID:             parent.RunID,
			ParentExecutionID: &parent.ID,
			AgentID:           task.AgentID,
			Status:            model.ExecutionStatusQueued,
			Version:           1,
			Task:              datatypes.JSON(delegation.Task),
		}
		if err := tx.Create(&child).Error; err != nil {
			return err
		}
		if err := s.appendEvent(
			tx,
			parent.RunID,
			parent.ID,
			model.EventTypeAgentCall,
			child.ID,
			delegation.Task,
		); err != nil {
			return err
		}
		suspendedPayload, err := json.Marshal(map[string]any{
			"waiting_on_execution_id": child.ID,
		})
		if err != nil {
			return err
		}
		if err := s.appendEvent(
			tx,
			parent.RunID,
			parent.ID,
			model.EventTypeExecutionSuspended,
			parent.ID,
			suspendedPayload,
		); err != nil {
			return err
		}
		parent.State = datatypes.JSON(delegation.State)
		parent.WaitingOnExecutionID = &child.ID
		parent.Status = model.ExecutionStatusWaiting
		parent.Version++
		parent.ClaimedBy = ""
		parent.ClaimedAt = nil
		parent.LeaseExpiresAt = nil
		if err := tx.Save(&parent).Error; err != nil {
			return err
		}
		if err := s.createDispatch(tx, child); err != nil {
			return err
		}
		delegated = DelegatedExecution{ExecutionID: child.ID, ParentVersion: parent.Version}
		return nil
	})
	return delegated, err
}

func (s *ExecutionService) GetExecutionResult(
	ctx context.Context,
	executionID string,
	parentExecutionID string,
	workerID string,
) (ExecutionResult, error) {
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(parentExecutionID, identifier.Execution) ||
		!identifier.IsValid(workerID, identifier.Worker) {
		return ExecutionResult{}, fmt.Errorf("%w: execution_id, parent_execution_id, or worker_id is invalid", ErrInvalid)
	}
	var child model.AgentExecution
	if err := s.database.WithContext(ctx).First(&child, "id = ?", executionID).Error; err != nil {
		return ExecutionResult{}, mapDatabaseError(err)
	}
	if child.ParentExecutionID == nil || *child.ParentExecutionID != parentExecutionID {
		return ExecutionResult{}, ErrConflict
	}
	var parent model.AgentExecution
	if err := s.database.WithContext(ctx).First(&parent, "id = ?", parentExecutionID).Error; err != nil {
		return ExecutionResult{}, mapDatabaseError(err)
	}
	if parent.Status != model.ExecutionStatusRunning ||
		parent.ClaimedBy != workerID ||
		!hasActiveLease(parent, time.Now().UTC()) {
		return ExecutionResult{}, ErrConflict
	}
	return ExecutionResult{Status: child.Status, Result: json.RawMessage(child.Result)}, nil
}

func (s *ExecutionService) CheckpointTool(ctx context.Context, executionID string, checkpoint ToolCheckpoint) (uint64, error) {
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(checkpoint.WorkerID), identifier.Worker) {
		return 0, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	var call toolCallPayload
	if err := json.Unmarshal(checkpoint.Call, &call); err != nil {
		return 0, fmt.Errorf("%w: call is invalid: %v", ErrInvalid, err)
	}
	var result toolResultPayload
	if err := json.Unmarshal(checkpoint.Result, &result); err != nil {
		return 0, fmt.Errorf("%w: result is invalid: %v", ErrInvalid, err)
	}
	if !identifier.IsValid(call.CallID, identifier.Call) ||
		!identifier.IsValid(call.RunID, identifier.Run) ||
		call.ParentExecutionID != executionID || call.ToolID == "" {
		return 0, fmt.Errorf("%w: call identity is invalid", ErrInvalid)
	}
	if result.CallID != call.CallID || result.RunID != call.RunID || result.ParentExecutionID != executionID {
		return 0, fmt.Errorf("%w: result identity does not match call", ErrInvalid)
	}
	state, err := decodeExecutionState(checkpoint.State)
	if err != nil {
		return 0, err
	}
	if state.Phase == "waiting_for_agent" {
		return 0, fmt.Errorf("%w: tool checkpoint state cannot be waiting for an agent", ErrInvalid)
	}
	var version uint64
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusRunning ||
			execution.ClaimedBy != checkpoint.WorkerID ||
			!hasActiveLease(execution, time.Now().UTC()) {
			return ErrConflict
		}
		if execution.RunID != call.RunID {
			return ErrConflict
		}
		if state.AgentID != execution.AgentID {
			return ErrConflict
		}
		var existing model.ToolCall
		lookup := tx.First(&existing, "id = ?", call.CallID)
		if lookup.Error == nil {
			version = execution.Version
			return nil
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		if execution.Version != checkpoint.ExpectedVersion {
			return ErrConflict
		}
		now := time.Now().UTC()
		record := model.ToolCall{
			ID:                call.CallID,
			RunID:             call.RunID,
			ParentExecutionID: call.ParentExecutionID,
			ToolID:            call.ToolID,
			Status:            result.Status,
			Arguments:         datatypes.JSON(call.Arguments),
			Output:            nullableJSON(result.Output),
			Error:             nullableJSON(result.Error),
			CompletedAt:       &now,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := s.appendEvent(tx, execution.RunID, execution.ID, model.EventTypeToolCall, call.CallID, checkpoint.Call); err != nil {
			return err
		}
		if err := s.appendEvent(tx, execution.RunID, execution.ID, model.EventTypeToolResult, call.CallID, checkpoint.Result); err != nil {
			return err
		}
		if err := s.workers.recordToolCall(tx, checkpoint.WorkerID); err != nil {
			return err
		}
		execution.State = datatypes.JSON(checkpoint.State)
		execution.WaitingOnExecutionID = nil
		leaseExpiresAt := time.Now().UTC().Add(s.leaseDuration)
		execution.LeaseExpiresAt = &leaseExpiresAt
		execution.Version++
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		version = execution.Version
		return nil
	})
	return version, err
}

func (s *ExecutionService) CheckpointModel(
	ctx context.Context,
	executionID string,
	checkpoint ModelCheckpoint,
) (uint64, error) {
	checkpoint.Model = strings.TrimSpace(checkpoint.Model)
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(checkpoint.WorkerID), identifier.Worker) ||
		checkpoint.Model == "" ||
		!identifier.IsOneOf(checkpoint.TurnMessageID, identifier.Message, identifier.Execution) ||
		checkpoint.Sequence == 0 {
		return 0, fmt.Errorf("%w: model, turn_message_id, and a positive sequence are required", ErrInvalid)
	}
	state, err := decodeExecutionState(checkpoint.State)
	if err != nil {
		return 0, err
	}
	if state.Phase == "waiting_for_agent" || !validJSONObject(checkpoint.Response) {
		return 0, fmt.Errorf("%w: model checkpoint response or state is invalid", ErrInvalid)
	}
	payload, err := json.Marshal(map[string]any{
		"model":           checkpoint.Model,
		"turn_message_id": checkpoint.TurnMessageID,
		"sequence":        checkpoint.Sequence,
		"input_tokens":    checkpoint.InputTokens,
		"output_tokens":   checkpoint.OutputTokens,
		"callable_ids":    checkpoint.CallableIDs,
		"has_content":     checkpoint.HasContent,
		"response":        checkpoint.Response,
	})
	if err != nil {
		return 0, err
	}
	var version uint64
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusRunning ||
			execution.ClaimedBy != checkpoint.WorkerID ||
			!hasActiveLease(execution, time.Now().UTC()) {
			return ErrConflict
		}
		if state.AgentID != execution.AgentID {
			return ErrConflict
		}
		if execution.ParentExecutionID == nil {
			var messageCount int64
			if err := tx.Model(&model.Message{}).
				Where(
					"id = ? AND parent_execution_id = ? AND role = ?",
					checkpoint.TurnMessageID,
					execution.ID,
					model.MessageRoleUser,
				).
				Count(&messageCount).Error; err != nil {
				return err
			}
			if messageCount == 0 {
				return ErrConflict
			}
		} else if checkpoint.TurnMessageID != execution.ID {
			return ErrConflict
		}
		var existing model.ExecutionEvent
		lookup := tx.Where(
			"execution_id = ? AND type = ? AND payload ->> 'turn_message_id' = ? AND payload ->> 'sequence' = ?",
			execution.ID,
			model.EventTypeModelResponse,
			checkpoint.TurnMessageID,
			fmt.Sprintf("%d", checkpoint.Sequence),
		).First(&existing)
		if lookup.Error == nil {
			version = execution.Version
			return nil
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		if execution.Version != checkpoint.ExpectedVersion {
			return ErrConflict
		}
		modelCallID := identifier.New(identifier.ModelCall)
		if err := s.appendEvent(
			tx,
			execution.RunID,
			execution.ID,
			model.EventTypeModelResponse,
			modelCallID,
			payload,
		); err != nil {
			return err
		}
		if err := s.workers.recordModelCall(
			tx,
			checkpoint.WorkerID,
			checkpoint.InputTokens,
			checkpoint.OutputTokens,
		); err != nil {
			return err
		}
		execution.State = datatypes.JSON(checkpoint.State)
		execution.WaitingOnExecutionID = nil
		leaseExpiresAt := time.Now().UTC().Add(s.leaseDuration)
		execution.LeaseExpiresAt = &leaseExpiresAt
		execution.Version++
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		version = execution.Version
		return nil
	})
	return version, err
}

func (s *ExecutionService) CompleteExecution(ctx context.Context, executionID string, completion Completion) (AppendedMessage, error) {
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(completion.WorkerID), identifier.Worker) {
		return AppendedMessage{}, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	content := strings.TrimSpace(completion.Content)
	if content == "" {
		return AppendedMessage{}, fmt.Errorf("%w: content cannot be empty", ErrInvalid)
	}
	var appended AppendedMessage
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		execution, err := s.lockOwnedExecution(tx, executionID, completion.WorkerID, completion.ExpectedVersion)
		if err != nil {
			return err
		}
		message := model.Message{
			ID:                identifier.New(identifier.Message),
			RunID:             execution.RunID,
			ParentExecutionID: execution.ID,
			Role:              model.MessageRoleAssistant,
			Content:           content,
		}
		payload := map[string]any{
			"message_id": message.ID,
			"role":       message.Role,
			"content":    message.Content,
			"usage":      completion.Usage,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		event := model.ExecutionEvent{
			ID:          identifier.New(identifier.Event),
			RunID:       execution.RunID,
			ExecutionID: execution.ID,
			Type:        model.EventTypeAssistantMessage,
			EntityID:    message.ID,
			Payload:     encoded,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		message.EventPosition = event.Position
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := s.workers.recordCompletion(tx, completion.WorkerID, model.ExecutionStatusSucceeded); err != nil {
			return err
		}
		execution.Status = model.ExecutionStatusIdle
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		execution.LeaseExpiresAt = nil
		execution.WaitingOnExecutionID = nil
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		appended = AppendedMessage{Message: message, EventPosition: event.Position, Version: execution.Version}
		return nil
	})
	return appended, err
}

func (s *ExecutionService) CompleteAgentExecution(
	ctx context.Context,
	executionID string,
	completion AgentCompletion,
) (model.ExecutionEvent, error) {
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(completion.WorkerID), identifier.Worker) {
		return model.ExecutionEvent{}, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	var result agentResultPayload
	if err := json.Unmarshal(completion.Result, &result); err != nil {
		return model.ExecutionEvent{}, fmt.Errorf("%w: result is invalid: %v", ErrInvalid, err)
	}
	if result.Status != model.ExecutionStatusSucceeded &&
		result.Status != model.ExecutionStatusFailed &&
		result.Status != model.ExecutionStatusCancelled {
		return model.ExecutionEvent{}, fmt.Errorf("%w: result status is not terminal", ErrInvalid)
	}
	var event model.ExecutionEvent
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		execution, err := s.lockOwnedExecution(tx, executionID, completion.WorkerID, completion.ExpectedVersion)
		if err != nil {
			return err
		}
		if execution.ParentExecutionID == nil ||
			result.RunID != execution.RunID ||
			result.ExecutionID != execution.ID ||
			result.ParentExecutionID == nil ||
			*result.ParentExecutionID != *execution.ParentExecutionID ||
			result.AgentID != execution.AgentID {
			return ErrConflict
		}

		event = model.ExecutionEvent{
			ID:          identifier.New(identifier.Event),
			RunID:       execution.RunID,
			ExecutionID: *execution.ParentExecutionID,
			Type:        model.EventTypeAgentResult,
			EntityID:    execution.ID,
			Payload:     datatypes.JSON(completion.Result),
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		execution.Result = datatypes.JSON(completion.Result)
		execution.Status = result.Status
		execution.CompletedAt = &now
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		execution.LeaseExpiresAt = nil
		if err := s.workers.recordCompletion(tx, completion.WorkerID, result.Status); err != nil {
			return err
		}
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		return s.resumeParent(tx, execution)
	})
	return event, err
}

func (s *ExecutionService) FailExecution(ctx context.Context, executionID string, failure Failure) (model.ExecutionEvent, error) {
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(strings.TrimSpace(failure.WorkerID), identifier.Worker) {
		return model.ExecutionEvent{}, fmt.Errorf("%w: execution_id or worker_id is invalid", ErrInvalid)
	}
	var event model.ExecutionEvent
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		execution, err := s.lockOwnedExecution(tx, executionID, failure.WorkerID, failure.ExpectedVersion)
		if err != nil {
			return err
		}
		isChild := execution.ParentExecutionID != nil
		if !isChild {
			payload, err := json.Marshal(map[string]any{"error": failure.Error})
			if err != nil {
				return err
			}
			event = model.ExecutionEvent{
				ID:          identifier.New(identifier.Event),
				RunID:       execution.RunID,
				ExecutionID: execution.ID,
				Type:        model.EventTypeExecutionFailed,
				EntityID:    execution.ID,
				Payload:     payload,
			}
			execution.Status = model.ExecutionStatusIdle
			execution.WaitingOnExecutionID = nil
		} else {
			payload, err := json.Marshal(map[string]any{
				"run_id":              execution.RunID,
				"execution_id":        execution.ID,
				"parent_execution_id": execution.ParentExecutionID,
				"agent_id":            execution.AgentID,
				"status":              model.ExecutionStatusFailed,
				"error":               failure.Error,
			})
			if err != nil {
				return err
			}
			event = model.ExecutionEvent{
				ID:          identifier.New(identifier.Event),
				RunID:       execution.RunID,
				ExecutionID: *execution.ParentExecutionID,
				Type:        model.EventTypeAgentResult,
				EntityID:    execution.ID,
				Payload:     payload,
			}
			now := time.Now().UTC()
			execution.Result = datatypes.JSON(payload)
			execution.Status = model.ExecutionStatusFailed
			execution.CompletedAt = &now
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		execution.LeaseExpiresAt = nil
		if err := s.workers.recordCompletion(tx, failure.WorkerID, model.ExecutionStatusFailed); err != nil {
			return err
		}
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		if isChild {
			return s.resumeParent(tx, execution)
		}
		return nil
	})
	return event, err
}

func (s *ExecutionService) resumeParent(tx *gorm.DB, child model.AgentExecution) error {
	if child.ParentExecutionID == nil {
		return ErrConflict
	}
	var parent model.AgentExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "id = ?", *child.ParentExecutionID).Error; err != nil {
		return mapDatabaseError(err)
	}
	if parent.Status != model.ExecutionStatusWaiting ||
		parent.WaitingOnExecutionID == nil ||
		*parent.WaitingOnExecutionID != child.ID {
		return ErrConflict
	}
	payload, err := json.Marshal(map[string]any{
		"completed_execution_id": child.ID,
		"status":                 child.Status,
	})
	if err != nil {
		return err
	}
	if err := s.appendEvent(
		tx,
		parent.RunID,
		parent.ID,
		model.EventTypeExecutionResumed,
		parent.ID,
		payload,
	); err != nil {
		return err
	}
	parent.Status = model.ExecutionStatusQueued
	parent.Version++
	parent.ClaimedBy = ""
	parent.ClaimedAt = nil
	parent.LeaseExpiresAt = nil
	if err := tx.Save(&parent).Error; err != nil {
		return err
	}
	return s.createDispatch(tx, parent)
}

func (s *ExecutionService) ListEvents(ctx context.Context, runID string, after uint64) ([]model.ExecutionEvent, error) {
	if !identifier.IsValid(runID, identifier.Run) {
		return nil, fmt.Errorf("%w: run_id is invalid", ErrInvalid)
	}
	var count int64
	if err := s.database.WithContext(ctx).Model(&model.Run{}).Where("id = ?", runID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNotFound
	}
	var events []model.ExecutionEvent
	if err := s.database.WithContext(ctx).
		Where("run_id = ? AND position > ?", runID, after).
		Order("position ASC").
		Limit(100).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func (s *ExecutionService) ListAllEvents(ctx context.Context, after uint64) ([]model.ExecutionEvent, error) {
	var events []model.ExecutionEvent
	if err := s.database.WithContext(ctx).
		Where("position > ?", after).
		Order("position ASC").
		Limit(100).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func (s *ExecutionService) createDispatch(tx *gorm.DB, execution model.AgentExecution) error {
	dispatchEventID := identifier.New(identifier.Event)
	payload, err := json.Marshal(map[string]any{
		"event_id":            dispatchEventID,
		"run_id":              execution.RunID,
		"execution_id":        execution.ID,
		"parent_execution_id": execution.ParentExecutionID,
	})
	if err != nil {
		return err
	}
	return tx.Create(&model.OutboxMessage{
		ID:          identifier.New(identifier.Outbox),
		ExecutionID: execution.ID,
		Topic:       s.executionReadyTopic,
		Payload:     payload,
	}).Error
}

func (s *ExecutionService) appendEvent(
	tx *gorm.DB,
	runID string,
	executionID string,
	eventType string,
	entityID string,
	payload []byte,
) error {
	event := model.ExecutionEvent{
		ID:          identifier.New(identifier.Event),
		RunID:       runID,
		ExecutionID: executionID,
		Type:        eventType,
		EntityID:    entityID,
		Payload:     payload,
	}
	return s.persistEvent(tx, &event)
}

func (s *ExecutionService) persistEvent(tx *gorm.DB, event *model.ExecutionEvent) error {
	if err := tx.Create(event).Error; err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return tx.Create(&model.OutboxMessage{
		ID:                   identifier.New(identifier.Outbox),
		ExecutionID:          event.ExecutionID,
		Topic:                s.runEventTopicPrefix + "/" + event.RunID + "/events",
		Payload:              payload,
		AcknowledgeOnPublish: true,
	}).Error
}

func (s *ExecutionService) lockOwnedExecution(
	tx *gorm.DB,
	executionID string,
	workerID string,
	expectedVersion uint64,
) (model.AgentExecution, error) {
	var execution model.AgentExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
		return model.AgentExecution{}, mapDatabaseError(err)
	}
	if execution.Status != model.ExecutionStatusRunning ||
		execution.ClaimedBy != workerID ||
		!hasActiveLease(execution, time.Now().UTC()) {
		return model.AgentExecution{}, ErrConflict
	}
	if execution.Version != expectedVersion {
		return model.AgentExecution{}, ErrConflict
	}
	return execution, nil
}

func hasActiveLease(execution model.AgentExecution, now time.Time) bool {
	return execution.LeaseExpiresAt != nil && execution.LeaseExpiresAt.After(now)
}

func nullableJSON(value json.RawMessage) datatypes.JSON {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return datatypes.JSON(value)
}

func validJSONObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func decodeExecutionState(value json.RawMessage) (agentExecutionStatePayload, error) {
	var state agentExecutionStatePayload
	if err := json.Unmarshal(value, &state); err != nil {
		return agentExecutionStatePayload{}, fmt.Errorf("%w: execution state is invalid: %v", ErrInvalid, err)
	}
	if state.SchemaVersion != 1 || !versionedIdentifierPattern.MatchString(state.AgentID) || state.Phase == "" {
		return agentExecutionStatePayload{}, fmt.Errorf("%w: execution state identity is invalid", ErrInvalid)
	}
	switch state.Phase {
	case "ready_for_model", "completed":
		if state.NextCallIndex != 0 || state.PendingAgentCall != nil {
			return agentExecutionStatePayload{}, fmt.Errorf("%w: execution state call position is invalid", ErrInvalid)
		}
	case "processing_calls":
		if state.PendingAgentCall != nil {
			return agentExecutionStatePayload{}, fmt.Errorf("%w: only a waiting state may contain a pending agent call", ErrInvalid)
		}
	case "waiting_for_agent":
		if state.PendingAgentCall == nil ||
			strings.TrimSpace(state.PendingAgentCall.RequestRef) == "" ||
			state.PendingAgentCall.Task.SchemaVersion != 1 ||
			state.PendingAgentCall.RequestIndex != state.NextCallIndex {
			return agentExecutionStatePayload{}, fmt.Errorf("%w: waiting state has an invalid pending agent call", ErrInvalid)
		}
	default:
		return agentExecutionStatePayload{}, fmt.Errorf("%w: execution state phase is invalid", ErrInvalid)
	}
	return state, nil
}

func mapDatabaseError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
