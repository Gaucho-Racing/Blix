package service

import (
	"context"
	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"time"
)

type ExecutionLeaseReaper struct {
	executions *ExecutionService
	interval   time.Duration
}

func NewExecutionLeaseReaper(s *ExecutionService, interval time.Duration) *ExecutionLeaseReaper {
	return &ExecutionLeaseReaper{executions: s, interval: interval}
}
func (r *ExecutionLeaseReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.executions.RecoverExpired(ctx); err != nil && ctx.Err() == nil {
			logger.SugarLogger.Errorf("recover executions: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
