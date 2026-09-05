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
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource state conflict")
	ErrInvalid  = errors.New("invalid request")
)

var versionedIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*_v[1-9][0-9]*$`)

type ExecutionService struct {
	database            *gorm.DB
	executionReadyTopic string
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
	RunID             string          `json:"run_id"`
	ExecutionID       string          `json:"execution_id"`
	ParentExecutionID *string         `json:"parent_execution_id"`
	AgentID           string          `json:"agent_id"`
	Version           uint64          `json:"version"`
	Messages          []model.Message `json:"messages"`
}

type ToolCheckpoint struct {
	WorkerID        string          `json:"worker_id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Call            json.RawMessage `json:"call"`
	Result          json.RawMessage `json:"result"`
}

type ModelCheckpoint struct {
	WorkerID        string   `json:"worker_id"`
	ExpectedVersion uint64   `json:"expected_version"`
	Model           string   `json:"model"`
	TurnMessageID   string   `json:"turn_message_id"`
	Sequence        uint64   `json:"sequence"`
	InputTokens     uint64   `json:"input_tokens"`
	OutputTokens    uint64   `json:"output_tokens"`
	CallableIDs     []string `json:"callable_ids"`
	HasContent      bool     `json:"has_content"`
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

type Failure struct {
	WorkerID        string         `json:"worker_id"`
	ExpectedVersion uint64         `json:"expected_version"`
	Error           map[string]any `json:"error"`
}

func NewExecutionService(database *gorm.DB, configuration config.Config) *ExecutionService {
	return &ExecutionService{
		database:            database,
		executionReadyTopic: configuration.MQTTExecutionReadyTopic,
	}
}

func (s *ExecutionService) CreateRun(ctx context.Context, agentID string) (CreatedRun, error) {
	agentID = strings.TrimSpace(agentID)
	if !versionedIdentifierPattern.MatchString(agentID) {
		return CreatedRun{}, fmt.Errorf("%w: agent_id must be a versioned identifier", ErrInvalid)
	}
	run := model.Run{
		ID:     identifier.New(),
		Kind:   model.RunKindConversation,
		Status: model.RunStatusActive,
	}
	execution := model.AgentExecution{
		ID:      identifier.New(),
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
		return tx.Create(&execution).Error
	})
	if err != nil {
		return CreatedRun{}, fmt.Errorf("create run: %w", err)
	}
	return CreatedRun{Run: run, Execution: execution}, nil
}

func (s *ExecutionService) AppendUserMessage(ctx context.Context, runID string, content string) (AppendedMessage, error) {
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
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", run.RootExecutionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusIdle {
			return ErrConflict
		}

		message := model.Message{
			ID:                identifier.New(),
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
			ID:          identifier.New(),
			RunID:       run.ID,
			ExecutionID: execution.ID,
			Type:        model.EventTypeUserMessage,
			EntityID:    message.ID,
			Payload:     payload,
		}
		if err := tx.Create(&event).Error; err != nil {
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
	if workerID == "" {
		return ExecutionContext{}, fmt.Errorf("%w: worker_id cannot be empty", ErrInvalid)
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
		now := time.Now().UTC()
		execution.Status = model.ExecutionStatusRunning
		execution.ClaimedBy = workerID
		execution.ClaimedAt = &now
		execution.Version++
		if err := tx.Save(&execution).Error; err != nil {
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
		claimed = ExecutionContext{
			RunID:             execution.RunID,
			ExecutionID:       execution.ID,
			ParentExecutionID: execution.ParentExecutionID,
			AgentID:           execution.AgentID,
			Version:           execution.Version,
			Messages:          messages,
		}
		return nil
	})
	if err != nil {
		return ExecutionContext{}, err
	}
	return claimed, nil
}

func (s *ExecutionService) CheckpointTool(ctx context.Context, executionID string, checkpoint ToolCheckpoint) (uint64, error) {
	var call toolCallPayload
	if err := json.Unmarshal(checkpoint.Call, &call); err != nil {
		return 0, fmt.Errorf("%w: call is invalid: %v", ErrInvalid, err)
	}
	var result toolResultPayload
	if err := json.Unmarshal(checkpoint.Result, &result); err != nil {
		return 0, fmt.Errorf("%w: result is invalid: %v", ErrInvalid, err)
	}
	if call.CallID == "" || call.RunID == "" || call.ParentExecutionID != executionID || call.ToolID == "" {
		return 0, fmt.Errorf("%w: call identity is invalid", ErrInvalid)
	}
	if result.CallID != call.CallID || result.RunID != call.RunID || result.ParentExecutionID != executionID {
		return 0, fmt.Errorf("%w: result identity does not match call", ErrInvalid)
	}
	var version uint64
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if execution.Status != model.ExecutionStatusRunning || execution.ClaimedBy != checkpoint.WorkerID {
			return ErrConflict
		}
		if execution.RunID != call.RunID {
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
	if checkpoint.Model == "" || checkpoint.TurnMessageID == "" || checkpoint.Sequence == 0 {
		return 0, fmt.Errorf("%w: model, turn_message_id, and a positive sequence are required", ErrInvalid)
	}
	payload, err := json.Marshal(map[string]any{
		"model":           checkpoint.Model,
		"turn_message_id": checkpoint.TurnMessageID,
		"sequence":        checkpoint.Sequence,
		"input_tokens":    checkpoint.InputTokens,
		"output_tokens":   checkpoint.OutputTokens,
		"callable_ids":    checkpoint.CallableIDs,
		"has_content":     checkpoint.HasContent,
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
		if execution.Status != model.ExecutionStatusRunning || execution.ClaimedBy != checkpoint.WorkerID {
			return ErrConflict
		}
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
		modelCallID := identifier.New()
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
			ID:                identifier.New(),
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
			ID:          identifier.New(),
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
		execution.Status = model.ExecutionStatusIdle
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		if err := tx.Save(&execution).Error; err != nil {
			return err
		}
		appended = AppendedMessage{Message: message, EventPosition: event.Position, Version: execution.Version}
		return nil
	})
	return appended, err
}

func (s *ExecutionService) FailExecution(ctx context.Context, executionID string, failure Failure) (model.ExecutionEvent, error) {
	var event model.ExecutionEvent
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		execution, err := s.lockOwnedExecution(tx, executionID, failure.WorkerID, failure.ExpectedVersion)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"error": failure.Error})
		if err != nil {
			return err
		}
		event = model.ExecutionEvent{
			ID:          identifier.New(),
			RunID:       execution.RunID,
			ExecutionID: execution.ID,
			Type:        model.EventTypeExecutionFailed,
			EntityID:    execution.ID,
			Payload:     payload,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		execution.Status = model.ExecutionStatusIdle
		execution.Version++
		execution.ClaimedBy = ""
		execution.ClaimedAt = nil
		return tx.Save(&execution).Error
	})
	return event, err
}

func (s *ExecutionService) ListEvents(ctx context.Context, runID string, after uint64) ([]model.ExecutionEvent, error) {
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

func (s *ExecutionService) createDispatch(tx *gorm.DB, execution model.AgentExecution) error {
	outboxID := identifier.New()
	payload, err := json.Marshal(map[string]any{
		"event_id":     outboxID,
		"run_id":       execution.RunID,
		"execution_id": execution.ID,
	})
	if err != nil {
		return err
	}
	return tx.Create(&model.OutboxMessage{
		ID:          outboxID,
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
	return tx.Create(&model.ExecutionEvent{
		ID:          identifier.New(),
		RunID:       runID,
		ExecutionID: executionID,
		Type:        eventType,
		EntityID:    entityID,
		Payload:     payload,
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
	if execution.Status != model.ExecutionStatusRunning || execution.ClaimedBy != workerID {
		return model.AgentExecution{}, ErrConflict
	}
	if execution.Version != expectedVersion {
		return model.AgentExecution{}, ErrConflict
	}
	return execution, nil
}

func nullableJSON(value json.RawMessage) datatypes.JSON {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return datatypes.JSON(value)
}

func mapDatabaseError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
