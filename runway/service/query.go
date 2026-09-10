package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type RunSummary struct {
	RunID             string                                  `json:"run_id"`
	Title             string                                  `json:"title"`
	CallerEntityID    string                                  `json:"caller_entity_id"`
	CallerContext     datatypes.JSONType[model.CallerContext] `json:"caller_context"`
	Kind              string                                  `json:"kind"`
	Status            string                                  `json:"status"`
	RootExecutionID   string                                  `json:"root_execution_id"`
	AgentID           string                                  `json:"agent_id"`
	ExecutionStatus   string                                  `json:"execution_status"`
	ExecutionCount    int64                                   `json:"execution_count"`
	EventCount        int64                                   `json:"event_count"`
	LastEventPosition uint64                                  `json:"last_event_position"`
	CreatedAt         time.Time                               `json:"created_at"`
	LastActivityAt    time.Time                               `json:"last_activity_at"`
}

type ExecutionSummary struct {
	ExecutionID          string     `json:"execution_id"`
	RunID                string     `json:"run_id"`
	ParentExecutionID    *string    `json:"parent_execution_id"`
	AgentID              string     `json:"agent_id"`
	Status               string     `json:"status"`
	Version              uint64     `json:"version"`
	ClaimedBy            string     `json:"claimed_by,omitempty"`
	ClaimedAt            *time.Time `json:"claimed_at,omitempty"`
	LeaseExpiresAt       *time.Time `json:"lease_expires_at,omitempty"`
	WaitingOnExecutionID *string    `json:"waiting_on_execution_id,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type RunDetails struct {
	Run        RunSummary         `json:"run"`
	Executions []ExecutionSummary `json:"executions"`
}

type ExecutionDetails struct {
	Execution               ExecutionSummary `json:"execution"`
	Task                    json.RawMessage  `json:"task,omitempty"`
	State                   json.RawMessage  `json:"state,omitempty"`
	Result                  json.RawMessage  `json:"result,omitempty"`
	HasProviderContinuation bool             `json:"has_provider_continuation"`
}

func (s *ExecutionService) ListRuns(
	ctx context.Context,
	limit int,
	callerEntityID string,
	includeAll bool,
) ([]RunSummary, error) {
	if limit < 1 || limit > 100 || !identifier.IsValid(callerEntityID, identifier.Entity) {
		return nil, ErrInvalid
	}
	whereClause := "WHERE r.caller_entity_id = ?"
	arguments := []any{callerEntityID, limit}
	if includeAll {
		whereClause = ""
		arguments = []any{limit}
	}
	var runs []RunSummary
	err := s.database.WithContext(ctx).Raw(`
		SELECT
			r.id AS run_id,
			COALESCE(
				(SELECT LEFT(message.content, 160) FROM runway_message message WHERE message.run_id = r.id AND message.role = 'user' ORDER BY message.event_position ASC LIMIT 1),
				'New conversation'
			) AS title,
			r.caller_entity_id,
			r.caller_context,
			r.kind,
			r.status,
			r.root_execution_id,
			root.agent_id,
			root.status AS execution_status,
			(SELECT COUNT(*) FROM runway_agent_execution execution WHERE execution.run_id = r.id) AS execution_count,
			(SELECT COUNT(*) FROM runway_execution_event event WHERE event.run_id = r.id) AS event_count,
			COALESCE((SELECT MAX(event.position) FROM runway_execution_event event WHERE event.run_id = r.id), 0) AS last_event_position,
			r.created_at,
			COALESCE(
				(SELECT MAX(event.created_at) FROM runway_execution_event event WHERE event.run_id = r.id),
				r.created_at
			) AS last_activity_at
		FROM runway_run r
		JOIN runway_agent_execution root ON root.id = r.root_execution_id
		`+whereClause+`
		ORDER BY last_activity_at DESC, r.id DESC
		LIMIT ?
	`, arguments...).Scan(&runs).Error
	return runs, err
}

func (s *ExecutionService) GetRun(ctx context.Context, runID string) (RunDetails, error) {
	if !identifier.IsValid(runID, identifier.Run) {
		return RunDetails{}, ErrInvalid
	}
	var run model.Run
	if err := s.database.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		return RunDetails{}, mapDatabaseError(err)
	}
	var executions []model.AgentExecution
	if err := s.database.WithContext(ctx).
		Where("run_id = ?", run.ID).
		Order("created_at ASC, id ASC").
		Find(&executions).Error; err != nil {
		return RunDetails{}, err
	}
	var eventCount int64
	if err := s.database.WithContext(ctx).
		Model(&model.ExecutionEvent{}).
		Where("run_id = ?", run.ID).
		Count(&eventCount).Error; err != nil {
		return RunDetails{}, err
	}
	lastActivityAt := run.CreatedAt
	var lastEventPosition uint64
	var latestEvent model.ExecutionEvent
	err := s.database.WithContext(ctx).
		Where("run_id = ?", run.ID).
		Order("position DESC").
		First(&latestEvent).Error
	if err == nil {
		lastActivityAt = latestEvent.CreatedAt
		lastEventPosition = latestEvent.Position
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return RunDetails{}, err
	}

	executionSummaries := make([]ExecutionSummary, 0, len(executions))
	var rootExecution *model.AgentExecution
	for index := range executions {
		execution := &executions[index]
		executionSummaries = append(executionSummaries, summarizeExecution(*execution))
		if execution.ID == run.RootExecutionID {
			rootExecution = execution
		}
	}
	if rootExecution == nil {
		return RunDetails{}, ErrConflict
	}
	title := "New conversation"
	var firstMessage model.Message
	err = s.database.WithContext(ctx).
		Where("run_id = ? AND role = ?", run.ID, model.MessageRoleUser).
		Order("event_position ASC").
		First(&firstMessage).Error
	if err == nil {
		title = truncateRunTitle(firstMessage.Content)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return RunDetails{}, err
	}
	return RunDetails{
		Run: RunSummary{
			RunID:             run.ID,
			Title:             title,
			CallerEntityID:    run.CallerEntityID,
			CallerContext:     run.CallerContext,
			Kind:              run.Kind,
			Status:            run.Status,
			RootExecutionID:   run.RootExecutionID,
			AgentID:           rootExecution.AgentID,
			ExecutionStatus:   rootExecution.Status,
			ExecutionCount:    int64(len(executions)),
			EventCount:        eventCount,
			LastEventPosition: lastEventPosition,
			CreatedAt:         run.CreatedAt,
			LastActivityAt:    lastActivityAt,
		},
		Executions: executionSummaries,
	}, nil
}

func truncateRunTitle(value string) string {
	characters := []rune(strings.TrimSpace(value))
	if len(characters) <= 160 {
		return string(characters)
	}
	return string(characters[:159]) + "…"
}

func (s *ExecutionService) GetExecution(ctx context.Context, executionID string) (ExecutionDetails, error) {
	if !identifier.IsValid(executionID, identifier.Execution) {
		return ExecutionDetails{}, ErrInvalid
	}
	var execution model.AgentExecution
	if err := s.database.WithContext(ctx).First(&execution, "id = ?", executionID).Error; err != nil {
		return ExecutionDetails{}, mapDatabaseError(err)
	}
	state, hasContinuation, err := stateForInspection(execution.State)
	if err != nil {
		return ExecutionDetails{}, err
	}
	return ExecutionDetails{
		Execution:               summarizeExecution(execution),
		Task:                    rawJSON(execution.Task),
		State:                   state,
		Result:                  rawJSON(execution.Result),
		HasProviderContinuation: hasContinuation,
	}, nil
}

func (s *ExecutionService) AuthorizeRunAccess(ctx context.Context, runID string, callerEntityID string, isAdmin bool) error {
	if !identifier.IsValid(runID, identifier.Run) || !identifier.IsValid(callerEntityID, identifier.Entity) {
		return ErrInvalid
	}
	var run model.Run
	if err := s.database.WithContext(ctx).Select("caller_entity_id").First(&run, "id = ?", runID).Error; err != nil {
		return mapDatabaseError(err)
	}
	if !isAdmin && run.CallerEntityID != callerEntityID {
		return ErrForbidden
	}
	return nil
}

func summarizeExecution(execution model.AgentExecution) ExecutionSummary {
	return ExecutionSummary{
		ExecutionID:          execution.ID,
		RunID:                execution.RunID,
		ParentExecutionID:    execution.ParentExecutionID,
		AgentID:              execution.AgentID,
		Status:               execution.Status,
		Version:              execution.Version,
		ClaimedBy:            execution.ClaimedBy,
		ClaimedAt:            execution.ClaimedAt,
		LeaseExpiresAt:       execution.LeaseExpiresAt,
		WaitingOnExecutionID: execution.WaitingOnExecutionID,
		CompletedAt:          execution.CompletedAt,
		CreatedAt:            execution.CreatedAt,
		UpdatedAt:            execution.UpdatedAt,
	}
}

func stateForInspection(value []byte) (json.RawMessage, bool, error) {
	if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
		return nil, false, nil
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(value, &state); err != nil {
		return nil, false, err
	}
	continuation, exists := state["continuation"]
	hasContinuation := exists && strings.TrimSpace(string(continuation)) != "null"
	delete(state, "continuation")
	encoded, err := json.Marshal(state)
	return encoded, hasContinuation, err
}

func rawJSON(value []byte) json.RawMessage {
	if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
		return nil
	}
	return json.RawMessage(value)
}
