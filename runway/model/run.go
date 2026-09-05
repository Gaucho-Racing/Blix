package model

import "time"

const (
	RunKindConversation = "conversation"
	RunStatusActive     = "active"
)

type Run struct {
	ID              string    `json:"run_id" gorm:"primaryKey"`
	Kind            string    `json:"kind" gorm:"index;not null"`
	RootExecutionID string    `json:"root_execution_id" gorm:"uniqueIndex;not null"`
	Status          string    `json:"status" gorm:"index;not null"`
	CreatedAt       time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Run) TableName() string {
	return "runway_run"
}
