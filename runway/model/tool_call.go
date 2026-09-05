package model

import (
	"time"

	"gorm.io/datatypes"
)

type ToolCall struct {
	ID                string         `json:"call_id" gorm:"primaryKey"`
	RunID             string         `json:"run_id" gorm:"index;not null"`
	ParentExecutionID string         `json:"parent_execution_id" gorm:"index;not null"`
	ToolID            string         `json:"tool_id" gorm:"index;not null"`
	Status            string         `json:"status" gorm:"index;not null"`
	Arguments         datatypes.JSON `json:"arguments" gorm:"type:jsonb;not null"`
	Output            datatypes.JSON `json:"output" gorm:"type:jsonb"`
	Error             datatypes.JSON `json:"error" gorm:"type:jsonb"`
	CreatedAt         time.Time      `json:"created_at" gorm:"autoCreateTime"`
	CompletedAt       *time.Time     `json:"completed_at,omitempty"`
}

func (ToolCall) TableName() string {
	return "runway_tool_call"
}
