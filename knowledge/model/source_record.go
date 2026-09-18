package model

import (
	"time"

	"gorm.io/datatypes"
)

type SourceRecord struct {
	ID                     string         `json:"source_record_id" gorm:"primaryKey"`
	SourceID               string         `json:"source_id" gorm:"index;uniqueIndex:idx_knowledge_source_record_external,priority:1;not null"`
	ExternalID             string         `json:"external_id" gorm:"uniqueIndex:idx_knowledge_source_record_external,priority:2;not null"`
	ExternalVersion        string         `json:"external_version" gorm:"uniqueIndex:idx_knowledge_source_record_external,priority:3;not null"`
	RecordType             string         `json:"record_type" gorm:"index;not null"`
	ParentExternalID       string         `json:"parent_external_id" gorm:"index;not null"`
	AuthorSourceIdentityID *string        `json:"author_source_identity_id,omitempty" gorm:"index"`
	ExternalURL            string         `json:"external_url" gorm:"type:text;not null"`
	Content                string         `json:"content" gorm:"type:text;not null"`
	EvidenceType           string         `json:"evidence_type" gorm:"not null;default:''"`
	CaptureMethod          string         `json:"capture_method" gorm:"not null;default:''"`
	CaptureRunID           string         `json:"capture_run_id" gorm:"not null;default:''"`
	CaptureExecutionID     string         `json:"capture_execution_id" gorm:"not null;default:''"`
	ContentHash            string         `json:"content_hash" gorm:"size:64;not null"`
	RecordHash             string         `json:"record_hash" gorm:"size:64;not null"`
	Metadata               datatypes.JSON `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	OccurredAt             time.Time      `json:"occurred_at" gorm:"index;not null"`
	IngestedAt             time.Time      `json:"ingested_at" gorm:"autoCreateTime"`
}

func (SourceRecord) TableName() string {
	return "knowledge_source_record"
}
