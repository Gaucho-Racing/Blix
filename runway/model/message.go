package model

import "time"

const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
)

type Message struct {
	ID                string    `json:"message_id" gorm:"primaryKey"`
	RunID             string    `json:"run_id" gorm:"index;not null"`
	ParentExecutionID string    `json:"parent_execution_id" gorm:"index;not null"`
	EventPosition     uint64    `json:"event_position" gorm:"uniqueIndex;not null"`
	Role              string    `json:"role" gorm:"index;not null"`
	Content           string    `json:"content" gorm:"type:text;not null"`
	CreatedAt         time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (Message) TableName() string {
	return "runway_message"
}
