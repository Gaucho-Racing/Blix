package model

import (
	"time"

	"gorm.io/datatypes"
)

type OutboxMessage struct {
	ID              string         `gorm:"primaryKey"`
	ExecutionID     string         `gorm:"index;not null"`
	Topic           string         `gorm:"index;not null"`
	Payload         datatypes.JSON `gorm:"type:jsonb;not null"`
	PublishAttempts uint64         `gorm:"not null"`
	LastPublishedAt *time.Time
	AcknowledgedAt  *time.Time `gorm:"index"`
	CreatedAt       time.Time  `gorm:"autoCreateTime"`
}

func (OutboxMessage) TableName() string {
	return "runway_outbox_message"
}
