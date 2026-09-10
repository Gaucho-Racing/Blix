package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	RunKindConversation      = "conversation"
	RunStatusActive          = "active"
	EntityTypeUser           = "USER"
	EntityTypeServiceAccount = "SERVICE_ACCOUNT"
)

type CallerContext struct {
	EntityID         string `json:"entity_id"`
	EntityType       string `json:"entity_type"`
	UserID           string `json:"user_id,omitempty"`
	ServiceAccountID string `json:"service_account_id,omitempty"`
}

type Run struct {
	ID              string                            `json:"run_id" gorm:"primaryKey"`
	CallerEntityID  string                            `json:"caller_entity_id" gorm:"index"`
	CallerContext   datatypes.JSONType[CallerContext] `json:"caller_context" gorm:"type:jsonb;not null;default:'{}'"`
	Kind            string                            `json:"kind" gorm:"index;not null"`
	RootExecutionID string                            `json:"root_execution_id" gorm:"uniqueIndex;not null"`
	Status          string                            `json:"status" gorm:"index;not null"`
	CreatedAt       time.Time                         `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time                         `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Run) TableName() string {
	return "runway_run"
}
