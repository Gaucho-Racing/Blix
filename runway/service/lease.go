package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const expiredLeaseBatchSize = 100

type ExecutionLeaseReaper struct {
	executions   *ExecutionService
	pollInterval time.Duration
}

func NewExecutionLeaseReaper(executions *ExecutionService, pollInterval time.Duration) *ExecutionLeaseReaper {
	return &ExecutionLeaseReaper{executions: executions, pollInterval: pollInterval}
}

func (r *ExecutionLeaseReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		r.recover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *ExecutionLeaseReaper) recover(ctx context.Context) {
	recovered, err := r.executions.RequeueExpiredExecutions(ctx, expiredLeaseBatchSize)
	if err != nil {
		if ctx.Err() == nil {
			logger.SugarLogger.Errorf("failed to recover expired execution leases: %v", err)
		}
		return
	}
	if recovered > 0 {
		logger.SugarLogger.Infof("requeued %d execution(s) with expired leases", recovered)
	}
}

func (s *ExecutionService) RequeueExpiredExecutions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	recovered := 0
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var executions []model.AgentExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"status = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)",
				model.ExecutionStatusRunning,
				now,
			).
			Order("lease_expires_at ASC").
			Limit(limit).
			Find(&executions).Error; err != nil {
			return err
		}
		for index := range executions {
			execution := &executions[index]
			payload, err := json.Marshal(map[string]any{
				"claimed_by":       execution.ClaimedBy,
				"claimed_at":       execution.ClaimedAt,
				"lease_expires_at": execution.LeaseExpiresAt,
			})
			if err != nil {
				return err
			}
			if err := s.appendEvent(
				tx,
				execution.RunID,
				execution.ID,
				model.EventTypeExecutionLeaseLost,
				execution.ID,
				payload,
			); err != nil {
				return err
			}
			execution.Status = model.ExecutionStatusQueued
			execution.Version++
			execution.ClaimedBy = ""
			execution.ClaimedAt = nil
			execution.LeaseExpiresAt = nil
			if err := tx.Save(execution).Error; err != nil {
				return err
			}
			if err := s.createDispatch(tx, *execution); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}
