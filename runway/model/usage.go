package model

import "time"

const (
	UsageCategoryModel     = "model"
	UsageCategoryTool      = "tool"
	UsageCategoryEmbedding = "embedding"
)

type UsageRecord struct {
	ID                string    `json:"usage_id" gorm:"primaryKey"`
	SourceKey         string    `json:"source_key" gorm:"uniqueIndex;not null"`
	Category          string    `json:"category" gorm:"not null;index:idx_usage_category_time,priority:1;check:chk_usage_category,category IN ('model','tool','embedding')"`
	Resource          string    `json:"resource" gorm:"not null;index"`
	RunID             *string   `json:"run_id,omitempty" gorm:"index"`
	SessionID         *string   `json:"session_id,omitempty" gorm:"index"`
	WorkerID          string    `json:"worker_id" gorm:"not null;default:'';index"`
	CallerEntityID    string    `json:"caller_entity_id" gorm:"not null;default:'';index"`
	ProfileID         string    `json:"profile_id" gorm:"not null;default:'';index"`
	InputTokens       uint64    `json:"input_tokens" gorm:"not null;default:0"`
	CachedInputTokens uint64    `json:"cached_input_tokens" gorm:"not null;default:0"`
	OutputTokens      uint64    `json:"output_tokens" gorm:"not null;default:0"`
	ReasoningTokens   uint64    `json:"reasoning_tokens" gorm:"not null;default:0"`
	Units             uint64    `json:"units" gorm:"not null;default:0"`
	CostNanos         int64     `json:"cost_nanos" gorm:"not null;default:0"`
	Priced            bool      `json:"priced" gorm:"not null"`
	PricingVersion    string    `json:"pricing_version" gorm:"not null"`
	CreatedAt         time.Time `json:"created_at" gorm:"not null;index;index:idx_usage_category_time,priority:2"`
}

func (UsageRecord) TableName() string { return "runway_usage_record" }
