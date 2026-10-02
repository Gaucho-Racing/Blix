package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	DocumentReviewPending  = "pending"
	DocumentReviewInReview = "in_review"
	DocumentReviewIngested = "ingested"
	DocumentReviewRejected = "rejected"
)

type Document struct {
	Stages            datatypes.JSONSlice[DocumentStage] `json:"stages" gorm:"type:jsonb;not null;default:'[]'"`
	ReviewStatus      string                             `json:"review_status" gorm:"index;not null;default:pending;check:chk_knowledge_document_review_status,review_status IN ('pending','in_review','ingested','rejected')"`
	ReviewedBy        string                             `json:"reviewed_by" gorm:"not null;default:''"`
	ReviewNote        string                             `json:"review_note" gorm:"type:text;not null;default:''"`
	ReviewedAt        *time.Time                         `json:"reviewed_at,omitempty"`
	ExtractedMetadata datatypes.JSON                     `json:"extracted_metadata" gorm:"type:jsonb"`
	MetadataOverrides datatypes.JSON                     `json:"metadata_overrides" gorm:"type:jsonb"`
	MetadataRevision  uint64                             `json:"metadata_revision" gorm:"not null;default:0"`
	DepotFileID       string                             `json:"depot_file_id" gorm:"index"`
	Status            string                             `json:"status" gorm:"index;not null;default:imported"`
	Objective         string                             `json:"objective" gorm:"type:text"`
	Caller            datatypes.JSON                     `json:"-" gorm:"type:jsonb"`
	Generation        uint64                             `json:"generation" gorm:"not null;default:0"`
	ConversationID    string                             `json:"conversation_id" gorm:"index:idx_knowledge_document_session,priority:2"`
	TaskID            string                             `json:"task_id"`
	SessionID         string                             `json:"-" gorm:"index:idx_knowledge_document_session,priority:1"`
	AttemptNumber     uint64                             `json:"-"`
	Error             string                             `json:"error"`
	UpdatedAt         time.Time                          `json:"updated_at"`
	ID                string                             `json:"document_id" gorm:"primaryKey"`
	SHA256            string                             `json:"sha256" gorm:"index;not null"`
	SourceID          string                             `json:"source_id" gorm:"index;not null"`
	Title             string                             `json:"title" gorm:"not null"`
	Filename          string                             `json:"filename" gorm:"not null"`
	Publisher         string                             `json:"publisher" gorm:"not null"`
	Version           string                             `json:"version" gorm:"not null"`
	Tags              datatypes.JSONSlice[string]        `json:"tags" gorm:"type:jsonb;not null;default:'[]'"`
	ExternalURL       string                             `json:"external_url" gorm:"not null"`
	PublishedAt       *time.Time                         `json:"published_at"`
	UploadedBy        string                             `json:"uploaded_by" gorm:"not null"`
	PageCount         int                                `json:"page_count" gorm:"not null"`
	SizeBytes         int64                              `json:"size_bytes" gorm:"not null"`
	CreatedAt         time.Time                          `json:"created_at" gorm:"autoCreateTime"`
}

type DocumentStage struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Generation  uint64     `json:"generation"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Document) TableName() string { return "knowledge_document" }

type DocumentPage struct {
	VisualSourceRecordID string `json:"visual_source_record_id" gorm:"index;not null;default:''"`
	DocumentID           string `json:"document_id" gorm:"primaryKey"`
	Number               int    `json:"page_number" gorm:"primaryKey"`
	SourceRecordID       string `json:"source_record_id" gorm:"uniqueIndex;not null"`
	CharacterCount       int    `json:"character_count" gorm:"not null"`
}

type DocumentPageImage struct {
	DocumentID  string    `json:"document_id" gorm:"primaryKey"`
	Number      int       `json:"page_number" gorm:"primaryKey"`
	PDFHash     string    `json:"pdf_hash" gorm:"not null"`
	Version     int       `json:"version" gorm:"not null"`
	SHA256      string    `json:"sha256" gorm:"not null"`
	DepotFileID string    `json:"depot_file_id" gorm:"not null"`
	Width       int       `json:"width" gorm:"not null"`
	Height      int       `json:"height" gorm:"not null"`
	Text        string    `json:"-" gorm:"type:text;not null"`
	NativeText  string    `json:"-" gorm:"type:text;not null"`
	TextMethod  string    `json:"text_method" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
}

func (DocumentPageImage) TableName() string { return "knowledge_document_page_image" }

func (DocumentPage) TableName() string { return "knowledge_document_page" }

type DocumentChunk struct {
	VisualFindings  datatypes.JSON `json:"visual_findings" gorm:"type:jsonb;not null;default:'[]'"`
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
