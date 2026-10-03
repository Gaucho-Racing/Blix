package model

import (
	pgvector "github.com/pgvector/pgvector-go"
	"time"
)

type Embedding struct {
	Namespace   string           `gorm:"primaryKey"`
	ContentHash string           `gorm:"primaryKey;size:64"`
	Content     string           `gorm:"type:text;not null"`
	Vector      *pgvector.Vector `gorm:"type:vector"`
	Status      string           `gorm:"not null;index"`
	Attempts    int              `gorm:"not null"`
	AvailableAt time.Time        `gorm:"not null;index"`
	LeaseUntil  *time.Time
	LeaseToken  string `gorm:"not null"`
	LastError   string `gorm:"not null"`
	UpdatedAt   time.Time
}

func (Embedding) TableName() string { return "knowledge_embedding" }
