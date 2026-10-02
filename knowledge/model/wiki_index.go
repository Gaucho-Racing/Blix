package model

import "time"

type WikiIndexState struct {
	ID        string `gorm:"primaryKey"`
	CommitSHA string `gorm:"not null"`
	IndexedAt time.Time
	CheckedAt time.Time
	LastError string `gorm:"type:text;not null"`
}

func (WikiIndexState) TableName() string { return "knowledge_wiki_index_state" }

type WikiPage struct {
	Path    string `gorm:"primaryKey"`
	BlobSHA string `gorm:"not null"`
	Title   string `gorm:"not null"`
	Content string `gorm:"type:text;not null"`
	Season  string `gorm:"index;not null"`
}

func (WikiPage) TableName() string { return "knowledge_wiki_page" }

type WikiSection struct {
	Path         string `gorm:"primaryKey"`
	Position     int    `gorm:"primaryKey"`
	Heading      string `gorm:"not null"`
	Content      string `gorm:"type:text;not null"`
	Start        int    `gorm:"not null"`
	End          int    `gorm:"not null"`
	Season       string `gorm:"index;not null"`
	SearchVector string `gorm:"type:tsvector;not null;default:'';->"`
}

func (WikiSection) TableName() string { return "knowledge_wiki_section" }
