package service

import (
	"context"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
)

type ReadyAttempt struct {
	ID          string `json:"attempt_id"`
	Queue       string `json:"queue"`
	RuntimeKind string `json:"runtime_kind"`
}

func (s *ExecutionService) ReadyAttempts(ctx context.Context, runtimeKind string, limit int) ([]ReadyAttempt, error) {
	if runtimeKind != "ingestion" || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	var attempts []model.ExecutionAttempt
	if err := s.database.WithContext(ctx).Where("status = ? AND runtime_kind = ? AND deadline_at > ?", model.StatusQueued, runtimeKind, time.Now().UTC()).Order("created_at, id").Limit(limit).Find(&attempts).Error; err != nil {
		return nil, err
	}
	result := make([]ReadyAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		result = append(result, ReadyAttempt{attempt.ID, attempt.Queue, attempt.RuntimeKind})
	}
	return result, nil
}
