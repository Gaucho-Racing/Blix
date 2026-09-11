package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	SourceStatusActive   = "active"
	SourceStatusDisabled = "disabled"
)

type Source struct {
	ID         string         `json:"source_id" gorm:"primaryKey"`
	Kind       string         `json:"kind" gorm:"uniqueIndex:idx_knowledge_source_external,priority:1;not null"`
	ExternalID string         `json:"external_id" gorm:"uniqueIndex:idx_knowledge_source_external,priority:2;not null"`
	Name       string         `json:"name" gorm:"type:text;not null"`
	Status     string         `json:"status" gorm:"index;not null"`
	Metadata   datatypes.JSON `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt  time.Time      `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt  time.Time      `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Source) TableName() string {
	return "knowledge_source"
}
