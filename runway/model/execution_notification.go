package model

import (
	"time"

	"gorm.io/datatypes"
)

type ExecutionNotification struct {
	ID               string         `json:"notification_id" gorm:"primaryKey"`
	RunID            string         `json:"run_id" gorm:"index;not null"`
	RootExecutionID  string         `json:"root_execution_id" gorm:"index;not null"`
	ChildExecutionID string         `json:"child_execution_id" gorm:"uniqueIndex;not null"`
	Status           string         `json:"status" gorm:"not null"`
	Result           datatypes.JSON `json:"result" gorm:"type:jsonb;not null"`
	DeliveredAt      *time.Time     `json:"delivered_at,omitempty" gorm:"index"`
	CreatedAt        time.Time      `json:"created_at" gorm:"autoCreateTime"`
}

func (ExecutionNotification) TableName() string {
	return "runway_execution_notification"
}
