package model

import "time"

type Document struct {
	ID          string    `json:"document_id" gorm:"primaryKey"`
	SHA256      string    `json:"sha256" gorm:"uniqueIndex;not null"`
	SourceID    string    `json:"source_id" gorm:"not null"`
	Title       string    `json:"title" gorm:"not null"`
	Filename    string    `json:"filename" gorm:"not null"`
	Publisher   string    `json:"publisher" gorm:"not null"`
	Version     string    `json:"version" gorm:"not null"`
	Kind        string    `json:"kind" gorm:"not null"`
	ExternalURL string    `json:"external_url" gorm:"not null"`
	PublishedAt time.Time `json:"published_at" gorm:"not null"`
	UploadedBy  string    `json:"uploaded_by" gorm:"not null"`
	PageCount   int       `json:"page_count" gorm:"not null"`
	SizeBytes   int64     `json:"size_bytes" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (Document) TableName() string { return "knowledge_document" }

type DocumentPage struct {
	DocumentID     string `json:"document_id" gorm:"primaryKey"`
	Number         int    `json:"page_number" gorm:"primaryKey"`
	SourceRecordID string `json:"source_record_id" gorm:"uniqueIndex;not null"`
	CharacterCount int    `json:"character_count" gorm:"not null"`
}

func (DocumentPage) TableName() string { return "knowledge_document_page" }
