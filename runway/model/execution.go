package model

import "time"

const (
	ExecutionStatusIdle    = "idle"
	ExecutionStatusQueued  = "queued"
	ExecutionStatusRunning = "running"
)

type AgentExecution struct {
	ID                string     `json:"execution_id" gorm:"primaryKey"`
	RunID             string     `json:"run_id" gorm:"index;not null"`
	ParentExecutionID *string    `json:"parent_execution_id" gorm:"index"`
	AgentID           string     `json:"agent_id" gorm:"index;not null"`
	Status            string     `json:"status" gorm:"index;not null"`
	Version           uint64     `json:"version" gorm:"not null"`
	ClaimedBy         string     `json:"claimed_by,omitempty" gorm:"index"`
	ClaimedAt         *time.Time `json:"claimed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (AgentExecution) TableName() string {
	return "runway_agent_execution"
}
