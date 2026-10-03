package model

import (
	"gorm.io/datatypes"
	"time"
)

type Inbox struct {
	ID                                    string `gorm:"primaryKey"`
	GuildID, ChannelID, AuthorID, Content string
	ReplyToMessageID                      string
	BindingID                             string         `gorm:"index"`
	TurnID                                string         `gorm:"index"`
	Payload                               datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	State                                 string         `gorm:"not null;default:pending;index"`
	Attempts                              int
	NextAttemptAt                         time.Time `gorm:"index"`
	ErrorCode                             string
	CreatedAt                             time.Time
}

func (Inbox) TableName() string { return "discord_inbox" }

type Binding struct {
	ID                                                       string `gorm:"primaryKey"`
	ChannelID                                                string `gorm:"index"`
	GuildID, RootChannelID, OwnerDiscordID, EntityID, UserID string
	RunID                                                    string `gorm:"index"`
	SessionID                                                string
	Cursor                                                   uint64
	LastPolledAt                                             time.Time `gorm:"index"`
	CreatedAt, UpdatedAt                                     time.Time
}

func (Binding) TableName() string { return "discord_run_binding" }

type Outbox struct {
	ID                        string `gorm:"primaryKey"`
	ChannelID, RunID, Content string
	DiscordMessageID          string `gorm:"index"`
	BindingID                 string `gorm:"index"`
	ReplyToMessageID          string
	Reaction                  string
	RemoveReaction            bool
	Placeholder               bool
	DeleteOutboxID            string
	WaitForOutboxID           string
	State                     string `gorm:"not null;default:pending;index"`
	Attempts                  int
	FirstAttemptAt            *time.Time
	NextAttemptAt             time.Time `gorm:"index"`
	ErrorCode                 string
	CreatedAt                 time.Time
}

func (Outbox) TableName() string { return "discord_outbox" }
