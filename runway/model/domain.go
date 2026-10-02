package model

import (
	"gorm.io/datatypes"
	"time"
)

const (
	EntityTypeUser           = "USER"
	EntityTypeServiceAccount = "SERVICE_ACCOUNT"
	StatusQueued             = "queued"
	StatusRunning            = "running"
	StatusWaiting            = "waiting"
	StatusSucceeded          = "succeeded"
	StatusFailed             = "failed"
	StatusCancelled          = "cancelled"
	StatusExpired            = "expired"
	StatusSuspended          = "suspended"
)

type CallerContext struct {
	EntityID         string `json:"entity_id"`
	EntityType       string `json:"entity_type"`
	UserID           string `json:"user_id,omitempty"`
	ServiceAccountID string `json:"service_account_id,omitempty"`
}

type Conversation struct {
	ID             string                            `json:"conversation_id" gorm:"primaryKey"`
	Name           string                            `json:"name" gorm:"not null"`
	CallerEntityID string                            `json:"caller_entity_id" gorm:"index;not null"`
	CallerContext  datatypes.JSONType[CallerContext] `json:"caller_context" gorm:"type:jsonb;not null"`
	LeadSessionID  string                            `json:"lead_session_id" gorm:"uniqueIndex;not null"`
	Status         string                            `json:"status" gorm:"not null"`
	CreatedAt      time.Time                         `json:"created_at"`
	UpdatedAt      time.Time                         `json:"updated_at"`
}

func (Conversation) TableName() string { return "runway_conversation" }

type AgentSession struct {
	ID              string         `json:"session_id" gorm:"primaryKey"`
	ConversationID  string         `json:"conversation_id" gorm:"index;not null"`
	ParentSessionID *string        `json:"parent_session_id,omitempty" gorm:"index"`
	ProfileID       string         `json:"profile_id" gorm:"not null"`
	ProfileVersion  uint64         `json:"profile_version" gorm:"not null"`
	RuntimeKind     string         `json:"runtime_kind" gorm:"not null"`
	WorkspaceID     *string        `json:"workspace_id,omitempty" gorm:"index"`
	Status          string         `json:"status" gorm:"not null"`
	Memory          datatypes.JSON `json:"memory" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (AgentSession) TableName() string { return "runway_agent_session" }

type ExecutionPolicy struct {
	Queue           string `json:"queue"`
	ModelProfile    string `json:"model_profile"`
	DeadlineSeconds uint64 `json:"deadline_seconds"`
	MaxAttempts     uint64 `json:"max_attempts"`
}

type Turn struct {
	ID             string                              `json:"turn_id" gorm:"primaryKey"`
	ConversationID string                              `json:"conversation_id" gorm:"index;not null"`
	SessionID      string                              `json:"session_id" gorm:"index;not null"`
	InputMessageID *string                             `json:"input_message_id,omitempty"`
	InputPosition  uint64                              `json:"input_position"`
	TriggerTaskID  *string                             `json:"trigger_task_id,omitempty" gorm:"uniqueIndex"`
	Notifications  datatypes.JSON                      `json:"notifications" gorm:"type:jsonb;not null;default:'[]'"`
	Status         string                              `json:"status" gorm:"index;not null"`
	WaitingTaskID  *string                             `json:"waiting_task_id,omitempty"`
	Policy         datatypes.JSONType[ExecutionPolicy] `json:"policy" gorm:"type:jsonb;not null"`
	DeadlineAt     time.Time                           `json:"deadline_at" gorm:"index;not null"`
	CompletedAt    *time.Time                          `json:"completed_at,omitempty"`
	CreatedAt      time.Time                           `json:"created_at"`
	UpdatedAt      time.Time                           `json:"updated_at"`
}

func (Turn) TableName() string { return "runway_turn" }

type Task struct {
	ID              string                              `json:"task_id" gorm:"primaryKey"`
	ConversationID  string                              `json:"conversation_id" gorm:"index;not null"`
	OriginTurnID    *string                             `json:"origin_turn_id,omitempty" gorm:"index"`
	ParentTaskID    *string                             `json:"parent_task_id,omitempty" gorm:"index"`
	ParentSessionID string                              `json:"parent_session_id" gorm:"index;not null"`
	SessionID       string                              `json:"session_id" gorm:"index;not null"`
	DelegationRef   string                              `json:"delegation_ref" gorm:"not null;uniqueIndex:idx_task_delegation,priority:2"`
	ParentTargetID  string                              `json:"parent_target_id" gorm:"not null;uniqueIndex:idx_task_delegation,priority:1"`
	Background      bool                                `json:"background" gorm:"not null"`
	Objective       string                              `json:"objective" gorm:"type:text;not null"`
	Input           datatypes.JSON                      `json:"input" gorm:"type:jsonb;not null"`
	Invocation      datatypes.JSON                      `json:"invocation" gorm:"type:jsonb;not null"`
	Status          string                              `json:"status" gorm:"index;not null"`
	WaitingTaskID   *string                             `json:"waiting_task_id,omitempty"`
	WaitingReason   string                              `json:"waiting_reason"`
	Policy          datatypes.JSONType[ExecutionPolicy] `json:"policy" gorm:"type:jsonb;not null"`
	DeadlineAt      time.Time                           `json:"deadline_at" gorm:"index;not null"`
	Result          datatypes.JSON                      `json:"result,omitempty" gorm:"type:jsonb"`
	Error           datatypes.JSON                      `json:"error,omitempty" gorm:"type:jsonb"`
	CompletedAt     *time.Time                          `json:"completed_at,omitempty"`
	CreatedAt       time.Time                           `json:"created_at"`
	UpdatedAt       time.Time                           `json:"updated_at"`
}

func (Task) TableName() string { return "runway_task" }

type ExecutionAttempt struct {
	ID             string         `json:"attempt_id" gorm:"primaryKey"`
	ConversationID string         `json:"conversation_id" gorm:"index;not null"`
	SessionID      string         `json:"session_id" gorm:"index;not null"`
	TargetID       string         `json:"target_id" gorm:"index;not null"`
	TurnID         *string        `json:"turn_id,omitempty"`
	TaskID         *string        `json:"task_id,omitempty"`
	Number         uint64         `json:"number" gorm:"not null"`
	Queue          string         `json:"queue" gorm:"index;not null"`
	RuntimeKind    string         `json:"runtime_kind" gorm:"not null"`
	Status         string         `json:"status" gorm:"index;not null"`
	WorkerID       string         `json:"worker_id" gorm:"index;not null"`
	FencingToken   string         `json:"fencing_token,omitempty"`
	Version        uint64         `json:"version" gorm:"not null"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty" gorm:"index"`
	DeadlineAt     time.Time      `json:"deadline_at" gorm:"index;not null"`
	RuntimeHandle  datatypes.JSON `json:"runtime_handle,omitempty" gorm:"type:jsonb"`
	Error          datatypes.JSON `json:"error,omitempty" gorm:"type:jsonb"`
	CompletedAt    *time.Time     `json:"completed_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func (ExecutionAttempt) TableName() string { return "runway_execution_attempt" }

type RuntimeCheckpoint struct {
	TargetID      string         `json:"target_id" gorm:"primaryKey"`
	SessionID     string         `json:"session_id" gorm:"index;not null"`
	RuntimeKind   string         `json:"runtime_kind" gorm:"not null"`
	SchemaVersion uint64         `json:"schema_version" gorm:"not null"`
	Payload       datatypes.JSON `json:"payload" gorm:"type:jsonb;not null"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (RuntimeCheckpoint) TableName() string { return "runway_runtime_checkpoint" }

type Message struct {
	ID             string    `json:"message_id" gorm:"primaryKey"`
	ConversationID string    `json:"conversation_id" gorm:"index;not null"`
	TurnID         string    `json:"turn_id" gorm:"index;not null"`
	SessionID      string    `json:"session_id" gorm:"not null"`
	EventPosition  uint64    `json:"event_position" gorm:"uniqueIndex;not null"`
	Role           string    `json:"role" gorm:"not null"`
	Content        string    `json:"content" gorm:"type:text;not null"`
	DisplayContent string    `json:"display_content,omitempty" gorm:"type:text;not null;default:''"`
	CreatedAt      time.Time `json:"created_at"`
}

func (Message) TableName() string { return "runway_message" }

type Event struct {
	Position       uint64         `json:"position" gorm:"primaryKey;autoIncrement"`
	ID             string         `json:"event_id" gorm:"uniqueIndex;not null"`
	ConversationID string         `json:"conversation_id" gorm:"index;not null"`
	SessionID      string         `json:"session_id" gorm:"index;not null"`
	TurnID         *string        `json:"turn_id,omitempty" gorm:"index"`
	TaskID         *string        `json:"task_id,omitempty" gorm:"index"`
	AttemptID      *string        `json:"attempt_id,omitempty"`
	Type           string         `json:"type" gorm:"index;not null"`
	EntityID       string         `json:"entity_id" gorm:"not null"`
	Payload        datatypes.JSON `json:"payload" gorm:"type:jsonb;not null"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (Event) TableName() string { return "runway_event" }

type OutboxMessage struct {
	ID                   string         `gorm:"primaryKey"`
	AttemptID            string         `gorm:"index;not null"`
	Topic                string         `gorm:"not null"`
	Payload              datatypes.JSON `gorm:"type:jsonb;not null"`
	AcknowledgeOnPublish bool           `gorm:"not null"`
	PublishAttempts      uint64         `gorm:"not null"`
	LastPublishedAt      *time.Time
	AcknowledgedAt       *time.Time `gorm:"index"`
	CreatedAt            time.Time
}

func (OutboxMessage) TableName() string { return "runway_outbox_message" }
