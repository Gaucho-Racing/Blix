package model

import "time"

type SourceChunk struct {
	ID             string       `json:"source_chunk_id" gorm:"primaryKey"`
	SourceRecordID string       `json:"source_record_id" gorm:"index;uniqueIndex:idx_source_chunk_position,priority:1;not null"`
	ChunkerVersion int          `json:"chunker_version" gorm:"uniqueIndex:idx_source_chunk_position,priority:2;not null"`
	Ordinal        int          `json:"ordinal" gorm:"uniqueIndex:idx_source_chunk_position,priority:3;not null"`
	StartOffset    int          `json:"start_offset" gorm:"not null"`
	EndOffset      int          `json:"end_offset" gorm:"not null"`
	Content        string       `json:"content" gorm:"type:text;not null"`
	ContentHash    string       `json:"content_hash" gorm:"size:64;not null"`
	RuleIDs        []string     `json:"rule_ids" gorm:"serializer:json;type:jsonb;not null;default:'[]'"`
	SearchVector   string       `json:"-" gorm:"type:tsvector;not null;default:'';->"`
	Record         SourceRecord `json:"-" gorm:"foreignKey:SourceRecordID;constraint:OnDelete:CASCADE"`
}

func (SourceChunk) TableName() string { return "knowledge_source_chunk" }

type SourceRecordIndex struct {
	SourceRecordID string       `gorm:"primaryKey"`
	ChunkerVersion int          `gorm:"primaryKey"`
	ContentHash    string       `gorm:"size:64;not null"`
	ChunkCount     int          `gorm:"not null"`
	IndexedAt      time.Time    `gorm:"autoCreateTime"`
	Record         SourceRecord `gorm:"foreignKey:SourceRecordID;constraint:OnDelete:CASCADE"`
}

func (SourceRecordIndex) TableName() string { return "knowledge_source_record_index" }
