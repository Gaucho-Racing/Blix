package model

import (
	"time"

	"gorm.io/datatypes"
)

type Document struct {
	DepotFileID    string         `json:"depot_file_id" gorm:"index"`
	Status         string         `json:"status" gorm:"index;not null;default:imported"`
	Objective      string         `json:"objective" gorm:"type:text"`
	Caller         datatypes.JSON `json:"-" gorm:"type:jsonb"`
	Generation     uint64         `json:"generation" gorm:"not null;default:0"`
	ConversationID string         `json:"conversation_id"`
	TaskID         string         `json:"task_id"`
	SessionID      string         `json:"-"`
	AttemptNumber  uint64         `json:"-"`
	Error          string         `json:"error"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ID             string         `json:"document_id" gorm:"primaryKey"`
	SHA256         string         `json:"sha256" gorm:"index;not null"`
	SourceID       string         `json:"source_id" gorm:"not null"`
	Title          string         `json:"title" gorm:"not null"`
	Filename       string         `json:"filename" gorm:"not null"`
	Publisher      string         `json:"publisher" gorm:"not null"`
	Version        string         `json:"version" gorm:"not null"`
	Kind           string         `json:"kind" gorm:"not null"`
	ExternalURL    string         `json:"external_url" gorm:"not null"`
	PublishedAt    time.Time      `json:"published_at" gorm:"not null"`
	UploadedBy     string         `json:"uploaded_by" gorm:"not null"`
	PageCount      int            `json:"page_count" gorm:"not null"`
	SizeBytes      int64          `json:"size_bytes" gorm:"not null"`
	CreatedAt      time.Time      `json:"created_at" gorm:"autoCreateTime"`
}

func (Document) TableName() string { return "knowledge_document" }

type DocumentPage struct {
	DocumentID     string `json:"document_id" gorm:"primaryKey"`
	Number         int    `json:"page_number" gorm:"primaryKey"`
	SourceRecordID string `json:"source_record_id" gorm:"uniqueIndex;not null"`
	CharacterCount int    `json:"character_count" gorm:"not null"`
}

func (DocumentPage) TableName() string { return "knowledge_document_page" }

type DocumentChunk struct {
	DocumentID      string         `json:"document_id" gorm:"primaryKey"`
	Number          int            `json:"number" gorm:"primaryKey"`
	FirstPage       int            `json:"first_page"`
	LastPage        int            `json:"last_page"`
	Evidence        datatypes.JSON `json:"-" gorm:"type:jsonb;not null"`
	Findings        string         `json:"findings" gorm:"type:text"`
	SourceRecordIDs datatypes.JSON `json:"source_record_ids" gorm:"type:jsonb"`
	CompletedAt     *time.Time     `json:"completed_at"`
}

func (DocumentChunk) TableName() string { return "knowledge_document_chunk" }

type DocumentNotesRevision struct {
	DocumentID   string    `json:"document_id" gorm:"primaryKey"`
	Revision     uint64    `json:"revision" gorm:"primaryKey"`
	Notes        string    `json:"notes" gorm:"type:text;not null"`
	InputTokens  uint64    `json:"input_tokens"`
	OutputTokens uint64    `json:"output_tokens"`
	CreatedAt    time.Time `json:"created_at"`
}

func (DocumentNotesRevision) TableName() string { return "knowledge_document_notes_revision" }
