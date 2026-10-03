package model

import (
	"gorm.io/datatypes"
	"time"
)

type Inbox struct {
	ID                                    string `gorm:"primaryKey"`
	GuildID, ChannelID, AuthorID, Content string
	Payload                               datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	State                                 string         `gorm:"not null;default:pending;index"`
	Attempts                              int
	NextAttemptAt                         time.Time `gorm:"index"`
	ErrorCode                             string
	CreatedAt                             time.Time
}

func (Inbox) TableName() string { return "discord_inbox" }

type Binding struct {
	ChannelID                                                string `gorm:"primaryKey"`
	GuildID, RootChannelID, OwnerDiscordID, EntityID, UserID string
	RunID                                                    string `gorm:"index"`
	SessionID                                                string
	Cursor                                                   uint64
	LastPolledAt                                             time.Time `gorm:"index"`
	CreatedAt, UpdatedAt                                     time.Time
}

func (Binding) TableName() string { return "discord_run_binding" }

type Outbox struct {
	ID                                          string `gorm:"primaryKey"`
	ChannelID, RunID, Content, DiscordMessageID string
	State                                       string `gorm:"not null;default:pending;index"`
	Attempts                                    int
	FirstAttemptAt                              *time.Time
	NextAttemptAt                               time.Time `gorm:"index"`
	ErrorCode                                   string
	CreatedAt                                   time.Time
}

func (Outbox) TableName() string { return "discord_outbox" }
