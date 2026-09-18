package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	ExecutionStatusIdle      = "idle"
	ExecutionStatusQueued    = "queued"
	ExecutionStatusRunning   = "running"
	ExecutionStatusWaiting   = "waiting"
	ExecutionStatusSucceeded = "succeeded"
	ExecutionStatusFailed    = "failed"
	ExecutionStatusCancelled = "cancelled"
)

type AgentExecution struct {
	ID                   string         `json:"execution_id" gorm:"primaryKey"`
	RunID                string         `json:"run_id" gorm:"index;not null"`
	ParentExecutionID    *string        `json:"parent_execution_id" gorm:"index"`
	DelegationRef        string         `json:"delegation_ref,omitempty" gorm:"index"`
	Detached             bool           `json:"detached" gorm:"index;not null;default:false"`
	AgentID              string         `json:"agent_id" gorm:"index;not null"`
	Status               string         `json:"status" gorm:"index;not null"`
	Version              uint64         `json:"version" gorm:"not null"`
	ClaimedBy            string         `json:"claimed_by,omitempty" gorm:"index"`
	ClaimedAt            *time.Time     `json:"claimed_at,omitempty"`
	LeaseExpiresAt       *time.Time     `json:"lease_expires_at,omitempty" gorm:"index"`
	Task                 datatypes.JSON `json:"task,omitempty" gorm:"type:jsonb"`
	State                datatypes.JSON `json:"state,omitempty" gorm:"type:jsonb"`
	Result               datatypes.JSON `json:"result,omitempty" gorm:"type:jsonb"`
	WaitingOnExecutionID *string        `json:"waiting_on_execution_id,omitempty" gorm:"index"`
	ActiveTurnMessageID  *string        `json:"active_turn_message_id,omitempty" gorm:"index"`
	NotificationCutoff   *time.Time     `json:"notification_cutoff,omitempty"`
	CompletedAt          *time.Time     `json:"completed_at,omitempty"`
	CreatedAt            time.Time      `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt            time.Time      `json:"updated_at" gorm:"autoUpdateTime"`
}

func (AgentExecution) TableName() string {
	return "runway_agent_execution"
}
