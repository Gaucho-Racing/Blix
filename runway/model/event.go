package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	EventTypeRunCreated         = "run.created_v1"
	EventTypeUserMessage        = "message.user_v1"
	EventTypeAssistantMessage   = "message.assistant_v1"
	EventTypeToolCall           = "tool.call_v1"
	EventTypeToolResult         = "tool.result_v1"
	EventTypeModelResponse      = "model.response_v1"
	EventTypeModelCall          = "model.call_v1"
	EventTypeModelFailure       = "model.failure_v1"
	EventTypeAgentCall          = "agent.call_v1"
	EventTypeAgentResult        = "agent.result_v1"
	EventTypeExecutionClaimed   = "execution.claimed_v1"
	EventTypeCallerVerified     = "caller.verified_v1"
	EventTypeExecutionSuspended = "execution.suspended_v1"
	EventTypeExecutionResumed   = "execution.resumed_v1"
	EventTypeExecutionLeaseLost = "execution.lease_expired_v1"
	EventTypeExecutionFailed    = "execution.failed_v1"
)

type ExecutionEvent struct {
	Position    uint64         `json:"position" gorm:"primaryKey;autoIncrement"`
	ID          string         `json:"event_id" gorm:"uniqueIndex;not null"`
	RunID       string         `json:"run_id" gorm:"index;not null"`
	ExecutionID string         `json:"execution_id" gorm:"index;not null"`
	Type        string         `json:"type" gorm:"index;not null"`
	EntityID    string         `json:"entity_id" gorm:"index;not null"`
	Payload     datatypes.JSON `json:"payload" gorm:"type:jsonb;not null"`
	CreatedAt   time.Time      `json:"created_at" gorm:"autoCreateTime"`
}

func (ExecutionEvent) TableName() string {
	return "runway_execution_event"
}
