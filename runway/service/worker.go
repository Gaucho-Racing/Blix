package service

import (
	"context"
	"fmt"
	"time"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	WorkerStatusOffline   = "offline"
	WorkerStatusIdle      = "idle"
	WorkerStatusActive    = "active"
	WorkerStatusSaturated = "saturated"
)

type WorkerService struct {
	database          *gorm.DB
	updates           *WorkerHub
	heartbeatInterval time.Duration
	offlineAfter      time.Duration
}

type WorkerRegistrationRequest struct {
	WorkerID    string `json:"worker_id"`
	Concurrency uint64 `json:"concurrency"`
}

type WorkerRegistration struct {
	WorkerID            string `json:"worker_id"`
	HeartbeatIntervalMS uint64 `json:"heartbeat_interval_ms"`
}

type WorkerSummary struct {
	WorkerID             string    `json:"worker_id"`
	Status               string    `json:"status"`
	Concurrency          uint64    `json:"concurrency"`
	ActiveExecutions     uint64    `json:"active_executions"`
	AvailableConcurrency uint64    `json:"available_concurrency"`
	ExecutionsClaimed    uint64    `json:"executions_claimed"`
	ExecutionsSucceeded  uint64    `json:"executions_succeeded"`
	ExecutionsFailed     uint64    `json:"executions_failed"`
	ExecutionsCancelled  uint64    `json:"executions_cancelled"`
	ModelCalls           uint64    `json:"model_calls"`
	ToolCalls            uint64    `json:"tool_calls"`
	InputTokens          uint64    `json:"input_tokens"`
	OutputTokens         uint64    `json:"output_tokens"`
	RegisteredAt         time.Time `json:"registered_at"`
	LastSeenAt           time.Time `json:"last_seen_at"`
	HeartbeatExpiresAt   time.Time `json:"heartbeat_expires_at"`
}

type workerSummaryRow struct {
	ID                  string
	Concurrency         uint64
	ExecutionsClaimed   uint64
	ExecutionsSucceeded uint64
	ExecutionsFailed    uint64
	ExecutionsCancelled uint64
	ModelCalls          uint64
	ToolCalls           uint64
	InputTokens         uint64
	OutputTokens        uint64
	RegisteredAt        time.Time
	LastSeenAt          time.Time
	ActiveExecutions    uint64
}

func NewWorkerService(database *gorm.DB, updates *WorkerHub, configuration config.Config) *WorkerService {
	return &WorkerService{
		database:          database,
		updates:           updates,
		heartbeatInterval: configuration.WorkerHeartbeatInterval,
		offlineAfter:      configuration.WorkerOfflineAfter,
	}
}

func (s *WorkerService) Register(ctx context.Context, request WorkerRegistrationRequest) (WorkerRegistration, error) {
	if !identifier.IsValid(request.WorkerID, identifier.Worker) || request.Concurrency == 0 || request.Concurrency > 1024 {
		return WorkerRegistration{}, fmt.Errorf("%w: worker_id or concurrency is invalid", ErrInvalid)
	}
	now := time.Now().UTC()
	worker := model.AgentWorker{
		ID:           request.WorkerID,
		Concurrency:  request.Concurrency,
		LastSeenAt:   now,
		RegisteredAt: now,
		UpdatedAt:    now,
	}
	err := s.database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"concurrency":  request.Concurrency,
			"last_seen_at": now,
			"updated_at":   now,
		}),
	}).Create(&worker).Error
	if err != nil {
		return WorkerRegistration{}, err
	}
	summary, err := s.Get(ctx, request.WorkerID)
	if err != nil {
		return WorkerRegistration{}, err
	}
	s.updates.Publish(summary)
	return WorkerRegistration{
		WorkerID:            request.WorkerID,
		HeartbeatIntervalMS: uint64(s.heartbeatInterval.Milliseconds()),
	}, nil
}

func (s *WorkerService) Heartbeat(ctx context.Context, workerID string) (WorkerSummary, error) {
	if !identifier.IsValid(workerID, identifier.Worker) {
		return WorkerSummary{}, fmt.Errorf("%w: worker_id is invalid", ErrInvalid)
	}
	now := time.Now().UTC()
	result := s.database.WithContext(ctx).Model(&model.AgentWorker{}).
		Where("id = ?", workerID).
		Updates(map[string]any{"last_seen_at": now, "updated_at": now})
	if result.Error != nil {
		return WorkerSummary{}, result.Error
	}
	if result.RowsAffected == 0 {
		return WorkerSummary{}, ErrNotFound
	}
	summary, err := s.Get(ctx, workerID)
	if err != nil {
		return WorkerSummary{}, err
	}
	s.updates.Publish(summary)
	return summary, nil
}

func (s *WorkerService) List(ctx context.Context, limit int) ([]WorkerSummary, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	var rows []workerSummaryRow
	if err := s.workerSummaryQuery(ctx).
		Order("worker.last_seen_at DESC, worker.id DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	workers := make([]WorkerSummary, 0, len(rows))
	for _, row := range rows {
		workers = append(workers, s.summarize(row, time.Now().UTC()))
	}
	return workers, nil
}

func (s *WorkerService) Get(ctx context.Context, workerID string) (WorkerSummary, error) {
	if !identifier.IsValid(workerID, identifier.Worker) {
		return WorkerSummary{}, fmt.Errorf("%w: worker_id is invalid", ErrInvalid)
	}
	var row workerSummaryRow
	result := s.workerSummaryQuery(ctx).Where("worker.id = ?", workerID).Scan(&row)
	if result.Error != nil {
		return WorkerSummary{}, result.Error
	}
	if result.RowsAffected == 0 {
		return WorkerSummary{}, ErrNotFound
	}
	return s.summarize(row, time.Now().UTC()), nil
}

func (s *WorkerService) workerSummaryQuery(ctx context.Context) *gorm.DB {
	return s.database.WithContext(ctx).
		Table("runway_agent_worker AS worker").
		Select(`
			worker.*,
			(
				SELECT COUNT(*)
				FROM runway_agent_execution execution
				WHERE execution.claimed_by = worker.id
					AND execution.status = ?
					AND execution.lease_expires_at > ?
			) AS active_executions
		`, model.ExecutionStatusRunning, time.Now().UTC())
}

func (s *WorkerService) summarize(row workerSummaryRow, now time.Time) WorkerSummary {
	heartbeatExpiresAt := row.LastSeenAt.Add(s.offlineAfter)
	status := WorkerStatusOffline
	available := uint64(0)
	if heartbeatExpiresAt.After(now) {
		if row.ActiveExecutions == 0 {
			status = WorkerStatusIdle
		} else if row.ActiveExecutions >= row.Concurrency {
			status = WorkerStatusSaturated
		} else {
			status = WorkerStatusActive
		}
		if row.ActiveExecutions < row.Concurrency {
			available = row.Concurrency - row.ActiveExecutions
		}
	}
	return WorkerSummary{
		WorkerID:             row.ID,
		Status:               status,
		Concurrency:          row.Concurrency,
		ActiveExecutions:     row.ActiveExecutions,
		AvailableConcurrency: available,
		ExecutionsClaimed:    row.ExecutionsClaimed,
		ExecutionsSucceeded:  row.ExecutionsSucceeded,
		ExecutionsFailed:     row.ExecutionsFailed,
		ExecutionsCancelled:  row.ExecutionsCancelled,
		ModelCalls:           row.ModelCalls,
		ToolCalls:            row.ToolCalls,
		InputTokens:          row.InputTokens,
		OutputTokens:         row.OutputTokens,
		RegisteredAt:         row.RegisteredAt,
		LastSeenAt:           row.LastSeenAt,
		HeartbeatExpiresAt:   heartbeatExpiresAt,
	}
}

func (s *WorkerService) recordClaim(tx *gorm.DB, workerID string) error {
	return s.increment(tx, workerID, map[string]any{
		"executions_claimed": gorm.Expr("executions_claimed + 1"),
	})
}

func (s *WorkerService) recordToolCall(tx *gorm.DB, workerID string) error {
	return s.increment(tx, workerID, map[string]any{
		"tool_calls": gorm.Expr("tool_calls + 1"),
	})
}

func (s *WorkerService) recordModelCall(tx *gorm.DB, workerID string, inputTokens uint64, outputTokens uint64) error {
	return s.increment(tx, workerID, map[string]any{
		"model_calls":   gorm.Expr("model_calls + 1"),
		"input_tokens":  gorm.Expr("input_tokens + ?", inputTokens),
		"output_tokens": gorm.Expr("output_tokens + ?", outputTokens),
	})
}

func (s *WorkerService) recordCompletion(tx *gorm.DB, workerID string, status string) error {
	column := "executions_failed"
	switch status {
	case model.ExecutionStatusSucceeded:
		column = "executions_succeeded"
	case model.ExecutionStatusCancelled:
		column = "executions_cancelled"
	}
	return s.increment(tx, workerID, map[string]any{
		column: gorm.Expr(column + " + 1"),
	})
}

func (s *WorkerService) increment(tx *gorm.DB, workerID string, updates map[string]any) error {
	now := time.Now().UTC()
	updates["last_seen_at"] = now
	updates["updated_at"] = now
	result := tx.Model(&model.AgentWorker{}).Where("id = ?", workerID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}
