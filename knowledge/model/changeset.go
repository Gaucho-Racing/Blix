package model

import "time"

const (
	ChangesetStatusProposed  = "proposed"
	ChangesetStatusCommitted = "committed"
	ChangesetStatusFailed    = "failed"

	ChangesetTypeContent    = "content"
	ChangesetTypeStructural = "structural"
)

type Changeset struct {
	ID              string     `json:"changeset_id" gorm:"primaryKey"`
	RunID           string     `json:"run_id" gorm:"index;not null"`
	ExecutionID     string     `json:"execution_id" gorm:"index;not null"`
	SeasonSlug      string     `json:"season_slug" gorm:"index;not null"`
	Type            string     `json:"type" gorm:"index;not null"`
	Status          string     `json:"status" gorm:"index;not null"`
	Summary         string     `json:"summary" gorm:"type:text;not null"`
	BaseCommitSHA   string     `json:"base_commit_sha" gorm:"size:64;not null"`
	ResultCommitSHA string     `json:"result_commit_sha" gorm:"size:64;not null"`
	Error           string     `json:"error" gorm:"type:text;not null"`
	CommittedAt     *time.Time `json:"committed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Changeset) TableName() string {
	return "knowledge_changeset"
}

type ChangesetFile struct {
	ID           string    `json:"changeset_file_id" gorm:"primaryKey"`
	ChangesetID  string    `json:"changeset_id" gorm:"uniqueIndex:idx_knowledge_changeset_file_path,priority:1;not null"`
	Operation    string    `json:"operation" gorm:"index;not null"`
	Path         string    `json:"path" gorm:"uniqueIndex:idx_knowledge_changeset_file_path,priority:2;type:text;not null"`
	PreviousPath string    `json:"previous_path" gorm:"type:text;not null"`
	Content      string    `json:"content" gorm:"type:text;not null"`
	ContentHash  string    `json:"content_hash" gorm:"size:64;not null"`
	CreatedAt    time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (ChangesetFile) TableName() string {
	return "knowledge_changeset_file"
}
