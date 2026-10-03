package model

import (
	"gorm.io/datatypes"
	"time"
)

type ChannelInputReceipt struct {
	ID             string         `gorm:"primaryKey"`
	CallerEntityID string         `gorm:"not null"`
	RequestHash    string         `gorm:"not null"`
	RunID          *string        `gorm:"index"`
	Response       datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt      time.Time
}

func (ChannelInputReceipt) TableName() string { return "runway_channel_input_receipt" }
