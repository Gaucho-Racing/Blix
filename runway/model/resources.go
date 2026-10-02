package model

import (
	"gorm.io/datatypes"
	"time"
)

type Workspace struct {
	ID          string     `json:"workspace_id" gorm:"primaryKey"`
	RunID       string     `json:"run_id" gorm:"index;not null"`
	BackendKind string     `json:"backend_kind" gorm:"not null"`
	BackendRef  string     `json:"backend_ref"`
	Status      string     `json:"status" gorm:"not null"`
	SnapshotRef string     `json:"snapshot_ref"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Workspace) TableName() string { return "runway_workspace" }

type WorkspaceRepository struct {
	ID            string    `json:"workspace_repository_id" gorm:"primaryKey"`
	WorkspaceID   string    `json:"workspace_id" gorm:"uniqueIndex:idx_workspace_repo,priority:1;not null"`
	RepositoryURL string    `json:"repository_url" gorm:"uniqueIndex:idx_workspace_repo,priority:2;not null"`
	CheckoutPath  string    `json:"checkout_path" gorm:"not null"`
	BaseSHA       string    `json:"base_sha"`
	WorkingBranch string    `json:"working_branch"`
	HeadSHA       string    `json:"head_sha"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (WorkspaceRepository) TableName() string { return "runway_workspace_repository" }

type Artifact struct {
	ID         string         `json:"artifact_id" gorm:"primaryKey"`
	RunID      string         `json:"run_id" gorm:"index;not null"`
	TaskID     *string        `json:"task_id,omitempty" gorm:"index"`
	AttemptID  string         `json:"attempt_id" gorm:"index;not null"`
	Kind       string         `json:"kind" gorm:"not null"`
	Content    datatypes.JSON `json:"content" gorm:"type:jsonb;not null"`
	StorageRef string         `json:"storage_ref,omitempty"`
	Metadata   datatypes.JSON `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (Artifact) TableName() string { return "runway_artifact" }

type Action struct {
	ReplaySafe     bool           `json:"replay_safe" gorm:"not null"`
	ID             string         `json:"action_id" gorm:"primaryKey"`
	RunID          string         `json:"run_id" gorm:"index;not null"`
	TargetID       string         `json:"target_id" gorm:"uniqueIndex:idx_action_key,priority:1;not null"`
	SessionID      string         `json:"session_id" gorm:"not null"`
	AttemptID      string         `json:"attempt_id" gorm:"not null"`
	IdempotencyKey string         `json:"idempotency_key" gorm:"uniqueIndex:idx_action_key,priority:2;not null"`
	ToolID         string         `json:"tool_id" gorm:"not null"`
	Arguments      datatypes.JSON `json:"arguments" gorm:"type:jsonb;not null"`
	Status         string         `json:"status" gorm:"not null"`
	Result         datatypes.JSON `json:"result,omitempty" gorm:"type:jsonb"`
	ExternalRef    string         `json:"external_ref,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func (Action) TableName() string { return "runway_action" }

type CapabilityGrant struct {
	ID        string         `json:"grant_id" gorm:"primaryKey"`
	RunID     string         `json:"run_id" gorm:"index;not null"`
	SessionID string         `json:"session_id" gorm:"index;not null;uniqueIndex:idx_session_tool,priority:1"`
	ToolID    string         `json:"tool_id" gorm:"not null;uniqueIndex:idx_session_tool,priority:2"`
	Scope     datatypes.JSON `json:"scope" gorm:"type:jsonb;not null;default:'{}'"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	RevokedAt *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (CapabilityGrant) TableName() string { return "runway_capability_grant" }

type Approval struct {
	ID                   string     `json:"approval_id" gorm:"primaryKey"`
	RunID                string     `json:"run_id" gorm:"index;not null"`
	ActionID             string     `json:"action_id" gorm:"uniqueIndex;not null"`
	Status               string     `json:"status" gorm:"not null"`
	RequestedBySessionID string     `json:"requested_by_session_id" gorm:"not null"`
	DecidedByEntityID    string     `json:"decided_by_entity_id,omitempty"`
	DecisionMessage      string     `json:"decision_message,omitempty"`
	DecidedAt            *time.Time `json:"decided_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

func (Approval) TableName() string { return "runway_approval" }

type SessionCommand struct {
	RequestRef     string         `json:"request_ref,omitempty" gorm:"index"`
	ID             string         `json:"command_id" gorm:"primaryKey"`
	SessionID      string         `json:"session_id" gorm:"index;not null"`
	TargetID       string         `json:"target_id" gorm:"index;not null"`
	Kind           string         `json:"kind" gorm:"not null"`
	Payload        datatypes.JSON `json:"payload" gorm:"type:jsonb;not null"`
	AcknowledgedAt *time.Time     `json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (SessionCommand) TableName() string { return "runway_session_command" }

type TurnTask struct {
	TurnID    string    `json:"turn_id" gorm:"primaryKey"`
	TaskID    string    `json:"task_id" gorm:"primaryKey"`
	Relation  string    `json:"relation" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
}

func (TurnTask) TableName() string { return "runway_turn_task" }
