package model

import "time"

const (
	EpisodeStatusDraft = "draft"
	EpisodeStatusReady = "ready"
)

type Episode struct {
	ID                       string     `json:"episode_id" gorm:"primaryKey"`
	Name                     string     `json:"name" gorm:"type:text;not null"`
	Summary                  string     `json:"summary" gorm:"type:text;not null"`
	SeasonSlug               string     `json:"season_slug" gorm:"index;not null"`
	Status                   string     `json:"status" gorm:"index;not null"`
	ReconstructionConfidence float64    `json:"reconstruction_confidence" gorm:"not null"`
	StartedAt                *time.Time `json:"started_at,omitempty" gorm:"index"`
	EndedAt                  *time.Time `json:"ended_at,omitempty" gorm:"index"`
	CreatedByConversationID  string     `json:"created_by_conversation_id" gorm:"index;not null"`
	CreatedBySessionID       string     `json:"created_by_session_id" gorm:"index;not null"`
	CreatedAt                time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt                time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Episode) TableName() string {
	return "knowledge_episode"
}

type EpisodeRecord struct {
	ID             string    `json:"episode_record_id" gorm:"primaryKey"`
	EpisodeID      string    `json:"episode_id" gorm:"uniqueIndex:idx_knowledge_episode_record,priority:1;uniqueIndex:idx_knowledge_episode_position,priority:1;not null"`
	SourceRecordID string    `json:"source_record_id" gorm:"uniqueIndex:idx_knowledge_episode_record,priority:2;index;not null"`
	Position       uint64    `json:"position" gorm:"uniqueIndex:idx_knowledge_episode_position,priority:2;not null"`
	CreatedAt      time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (EpisodeRecord) TableName() string {
	return "knowledge_episode_record"
}

type EpisodeParticipant struct {
	ID               string    `json:"episode_participant_id" gorm:"primaryKey"`
	EpisodeID        string    `json:"episode_id" gorm:"uniqueIndex:idx_knowledge_episode_participant,priority:1;not null"`
	SourceIdentityID string    `json:"source_identity_id" gorm:"uniqueIndex:idx_knowledge_episode_participant,priority:2;index;not null"`
	EntityID         *string   `json:"entity_id,omitempty" gorm:"index"`
	Role             string    `json:"role" gorm:"uniqueIndex:idx_knowledge_episode_participant,priority:3;not null"`
	CreatedAt        time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (EpisodeParticipant) TableName() string {
	return "knowledge_episode_participant"
}
