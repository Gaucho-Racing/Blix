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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ModelAttempt struct {
	WorkerID        string             `json:"worker_id"`
	ExpectedVersion uint64             `json:"expected_version"`
	Type            string             `json:"type"`
	CallID          string             `json:"call_id"`
	Model           string             `json:"model"`
	TurnMessageID   string             `json:"turn_message_id"`
	Sequence        uint64             `json:"sequence"`
	Attempt         uint64             `json:"attempt"`
	MaxAttempts     uint64             `json:"max_attempts"`
	Request         json.RawMessage    `json:"request"`
	ElapsedMS       uint64             `json:"elapsed_ms"`
	Error           *ModelAttemptError `json:"error"`
	HTTPStatus      *int               `json:"http_status"`
	RetryInSeconds  *float64           `json:"retry_in_seconds"`
}

type ModelAttemptError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *ExecutionService) RecordModelAttempt(ctx context.Context, executionID string, attempt ModelAttempt) (uint64, error) {
	attempt.Model = strings.TrimSpace(attempt.Model)
	if !identifier.IsValid(executionID, identifier.Execution) ||
		!identifier.IsValid(attempt.WorkerID, identifier.Worker) ||
		!identifier.IsValid(attempt.CallID, identifier.ModelCall) ||
		!identifier.IsOneOf(attempt.TurnMessageID, identifier.Message, identifier.Execution) ||
		attempt.Model == "" || attempt.Sequence == 0 || attempt.Attempt == 0 || attempt.Attempt > attempt.MaxAttempts {
		return 0, fmt.Errorf("%w: invalid model attempt identifiers or sequence", ErrInvalid)
	}
	if attempt.Type != model.EventTypeModelCall && attempt.Type != model.EventTypeModelFailure {
		return 0, fmt.Errorf("%w: invalid model attempt event type", ErrInvalid)
	}
	if !validJSONObject(attempt.Request) ||
		(attempt.Type == model.EventTypeModelCall && (attempt.Error != nil || attempt.RetryInSeconds != nil)) ||
		(attempt.Type == model.EventTypeModelFailure && (attempt.Error == nil || attempt.Error.Code == "" || attempt.Error.Message == "")) ||
		(attempt.HTTPStatus != nil && (*attempt.HTTPStatus < 100 || *attempt.HTTPStatus > 599)) ||
		(attempt.RetryInSeconds != nil && (*attempt.RetryInSeconds < 0 || !attempt.Error.Retryable || attempt.Attempt == attempt.MaxAttempts)) {
		return 0, fmt.Errorf("%w: invalid model attempt payload", ErrInvalid)
	}
	payload, err := json.Marshal(map[string]any{
		"call_id": attempt.CallID, "model": attempt.Model, "turn_message_id": attempt.TurnMessageID,
		"sequence": attempt.Sequence, "attempt": attempt.Attempt, "max_attempts": attempt.MaxAttempts,
		"request": attempt.Request, "elapsed_ms": attempt.ElapsedMS, "error": attempt.Error,
		"http_status": attempt.HTTPStatus, "retry_in_seconds": attempt.RetryInSeconds,
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
		if execution.Status != model.ExecutionStatusRunning || execution.ClaimedBy != attempt.WorkerID ||
			!hasActiveLease(execution, time.Now().UTC()) {
			return ErrConflict
		}
		if err := validateModelTurn(tx, execution, attempt.TurnMessageID); err != nil {
			return err
		}
		var existing model.ExecutionEvent
		lookup := tx.Where("execution_id = ? AND entity_id = ? AND type = ?", execution.ID, attempt.CallID, attempt.Type).First(&existing)
		if lookup.Error == nil {
			var matches int64
			if err := tx.Model(&model.ExecutionEvent{}).Where("id = ? AND payload = ?::jsonb", existing.ID, string(payload)).Count(&matches).Error; err != nil {
				return err
			}
			if matches != 1 {
				return ErrConflict
			}
			version = execution.Version
			return nil
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		if execution.Version != attempt.ExpectedVersion {
			return ErrConflict
		}
		if attempt.Type == model.EventTypeModelFailure {
			if err := validateModelAttemptResult(tx, execution.ID, attempt.CallID, attempt.Model, attempt.TurnMessageID, attempt.Sequence, attempt.Attempt); err != nil {
				return err
			}
		}
		if err := s.appendEvent(tx, execution.RunID, execution.ID, attempt.Type, attempt.CallID, payload); err != nil {
			return err
		}
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

func validateModelTurn(tx *gorm.DB, execution model.AgentExecution, turnMessageID string) error {
	if execution.ParentExecutionID != nil {
		if turnMessageID != execution.ID {
			return ErrConflict
		}
		return nil
	}
	var count int64
	if err := tx.Model(&model.Message{}).Where(
		"id = ? AND parent_execution_id = ? AND role = ?", turnMessageID, execution.ID, model.MessageRoleUser,
	).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func validateModelAttemptResult(tx *gorm.DB, executionID, callID, modelName, turnMessageID string, sequence, attempt uint64) error {
	var call model.ExecutionEvent
	if err := tx.Where("execution_id = ? AND entity_id = ? AND type = ?", executionID, callID, model.EventTypeModelCall).First(&call).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		return err
	}
	var request ModelAttempt
	if err := json.Unmarshal(call.Payload, &request); err != nil {
		return err
	}
	if request.Model != modelName || request.TurnMessageID != turnMessageID || request.Sequence != sequence || request.Attempt != attempt {
		return ErrConflict
	}
	var count int64
	if err := tx.Model(&model.ExecutionEvent{}).Where(
		"execution_id = ? AND entity_id = ? AND type IN ?", executionID, callID, []string{model.EventTypeModelResponse, model.EventTypeModelFailure},
	).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}
