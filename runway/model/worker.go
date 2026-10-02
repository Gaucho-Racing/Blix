package model

import "time"

type AgentWorker struct {
	RuntimeKind           string    `json:"runtime_kind" gorm:"not null;default:api"`
	ID                    string    `json:"worker_id" gorm:"primaryKey"`
	BackgroundConcurrency uint64    `json:"background_concurrency" gorm:"not null"`
	Concurrency           uint64    `json:"concurrency" gorm:"not null"`
	ExecutionsClaimed     uint64    `json:"executions_claimed" gorm:"not null;default:0"`
	ExecutionsSucceeded   uint64    `json:"executions_succeeded" gorm:"not null;default:0"`
	ExecutionsFailed      uint64    `json:"executions_failed" gorm:"not null;default:0"`
	ExecutionsCancelled   uint64    `json:"executions_cancelled" gorm:"not null;default:0"`
	ModelCalls            uint64    `json:"model_calls" gorm:"not null;default:0"`
	ToolCalls             uint64    `json:"tool_calls" gorm:"not null;default:0"`
	InputTokens           uint64    `json:"input_tokens" gorm:"not null;default:0"`
	OutputTokens          uint64    `json:"output_tokens" gorm:"not null;default:0"`
	RegisteredAt          time.Time `json:"registered_at" gorm:"autoCreateTime"`
	LastSeenAt            time.Time `json:"last_seen_at" gorm:"index;not null"`
	UpdatedAt             time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (AgentWorker) TableName() string {
	return "runway_agent_worker"
}
