package model

import "time"

const (
	IdentityResolutionUnresolved  = "unresolved"
	IdentityResolutionResolved    = "resolved"
	IdentityResolutionAmbiguous   = "ambiguous"
	IdentityResolutionUnsupported = "unsupported"
)

type SourceIdentity struct {
	ID               string     `json:"source_identity_id" gorm:"primaryKey"`
	Provider         string     `json:"provider" gorm:"uniqueIndex:idx_knowledge_source_identity_external,priority:1;not null"`
	ExternalID       string     `json:"external_id" gorm:"uniqueIndex:idx_knowledge_source_identity_external,priority:2;not null"`
	DisplayName      string     `json:"display_name" gorm:"type:text;not null"`
	Username         string     `json:"username" gorm:"type:text;not null"`
	EntityID         *string    `json:"entity_id,omitempty" gorm:"index"`
	ResolutionStatus string     `json:"resolution_status" gorm:"index;not null"`
	ResolutionMethod string     `json:"resolution_method" gorm:"type:text;not null"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	LastObservedAt   time.Time  `json:"last_observed_at" gorm:"not null"`
	CreatedAt        time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (SourceIdentity) TableName() string {
	return "knowledge_source_identity"
}
